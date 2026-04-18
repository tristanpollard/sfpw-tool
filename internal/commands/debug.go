package commands

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/tristanpollard/sfpw-tool/internal/ble"
	"github.com/tristanpollard/sfpw-tool/internal/eeprom"
	"github.com/tristanpollard/sfpw-tool/internal/firmware"
	"github.com/tristanpollard/sfpw-tool/internal/protocol"
	"github.com/tristanpollard/sfpw-tool/internal/util"

	"tinygo.org/x/bluetooth"
)

// TestEncode tests the encoding without connecting to device
func TestEncode() {
	// Use the same JSON as captured from official app
	// {"type":"httpRequest","id":"00000000-0000-0000-0000-000000000003","timestamp":1768449224138,"method":"POST","path":"/api/1.0/deadbeefcafe/sif/start","headers":{}}

	req := protocol.APIRequest{
		Type:      "httpRequest",
		ID:        "00000000-0000-0000-0000-000000000005",
		Timestamp: 1768449227468,
		Method:    "GET",
		Path:      "/api/1.0/deadbeefcafe/stats",
	}
	/*
		{
			"type":"httpRequest",
			"id":"00000000-0000-0000-0000-000000000005",
			"timestamp":1768449227468,
			"method":"GET",
			"path":"/api/1.0/deadbeefcafe/stats",
			"headers":{}
		}
	*/

	jsonData, _ := json.Marshal(req)
	fmt.Fprintf(os.Stderr, "JSON (%d bytes): %s\n\n", len(jsonData), string(jsonData))

	// Use seqNum 5 to match the captured request ID
	encoded, err := protocol.BinmeEncode(jsonData, nil, 5)
	if err != nil {
		log.Fatal("Encode failed: ", err)
	}

	fmt.Fprintf(os.Stderr, "Encoded (%d bytes):\n%X\n\n", len(encoded), encoded)

	// Now decode it back
	headerJSON, bodyData, err := protocol.BinmeDecode(encoded)
	if err != nil {
		log.Fatal("Decode failed:", err)
	}

	fmt.Fprintf(os.Stderr, "Decoded header (%d bytes): %s\n", len(headerJSON), string(headerJSON))
	fmt.Fprintf(os.Stderr, "Decoded body (%d bytes): %X\n\n", len(bodyData), bodyData)

	// Print the captured packet for comparison
	fmt.Fprintln(os.Stderr, "=== Captured from official app ===")
	captured := "009a000503010101000000007d789c6d8cb10ec2300c44ffc573214d1592901db157fc804b8d9221c210335455ff1d2375e48693eef4ee569085091264111ee9f5a126d04199b5ea771dfed8ae93b252aa8eb032241b7c74ee3c0cc1f9d84125c9cfdfd3f572539051b206835c8c3df6c6de3dda293c268ad1e883348532e14cef0669ddb62ffb322b7a0201010000000008789c030000000001"
	fmt.Fprintf(os.Stderr, "Captured: %s\n", captured)

	byteArray, err := hex.DecodeString(captured)
	if err != nil {
		log.Fatal(err)
	}

	headerJSON, bodyData, err = protocol.BinmeDecode(byteArray)
	if err != nil {
		log.Fatal("Decode failed:", err)
	}
	fmt.Fprintf(os.Stderr, "Decoded header (%d bytes): %s\n", len(headerJSON), string(headerJSON))
	fmt.Fprintf(os.Stderr, "Decoded body (%d bytes): %X\n\n", len(bodyData), bodyData)
}

// TestPackets reads packets from a TSV file and decodes each one
func TestPackets(filename string) {
	file, err := os.Open(filename)
	if err != nil {
		log.Fatal("Failed to open file:", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	// Increase buffer size for large packets
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 64*1024)

	lineNum := 0
	successCount := 0
	failCount := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if line == "" {
			continue
		}

		// Parse TSV: frame_num \t src \t dst \t hex
		parts := strings.Split(line, "\t")
		if len(parts) < 4 {
			fmt.Fprintf(os.Stderr, "Line %d: invalid format (expected 4 columns, got %d)\n", lineNum, len(parts))
			failCount++
			continue
		}

		frameNum := parts[0]
		src := parts[1]
		dst := parts[2]
		hexData := parts[3]

		// Determine direction
		direction := "???"
		if strings.Contains(src, "Ubiquiti") {
			direction = "RSP"
		} else if strings.Contains(dst, "Ubiquiti") {
			direction = "REQ"
		}

		// Skip very short packets (like 0100ffff)
		if len(hexData) < 16 {
			fmt.Fprintf(os.Stderr, "Frame %s [%s]: too short (%d hex chars), skipping\n", frameNum, direction, len(hexData))
			continue
		}

		// Decode hex
		data, err := hex.DecodeString(hexData)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Frame %s [%s]: hex decode error: %v\n", frameNum, direction, err)
			failCount++
			continue
		}

		// Try to decode as binme packet
		headerJSON, bodyData, err := protocol.BinmeDecode(data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Frame %s [%s]: decode error: %v\n", frameNum, direction, err)
			failCount++
			continue
		}

		// Parse the header JSON to get type and path/status
		var envelope map[string]any
		if err := json.Unmarshal(headerJSON, &envelope); err != nil {
			fmt.Fprintf(os.Stderr, "Frame %s [%s]: JSON parse error: %v\n", frameNum, direction, err)
			failCount++
			continue
		}

		// Extract relevant fields
		msgType, _ := envelope["type"].(string)
		id, _ := envelope["id"].(string)
		// Get last 4 chars of ID for display
		shortID := id
		if len(id) > 4 {
			shortID = id[len(id)-4:]
		}

		var summary string
		if msgType == "httpRequest" {
			method, _ := envelope["method"].(string)
			path, _ := envelope["path"].(string)
			summary = fmt.Sprintf("%s %s", method, path)
		} else if msgType == "httpResponse" {
			statusCode, _ := envelope["statusCode"].(float64)
			summary = fmt.Sprintf("status=%d", int(statusCode))
		} else {
			summary = msgType
		}

		// Format body
		bodyStr := ""
		if len(bodyData) > 0 {
			if len(bodyData) > 60 {
				bodyStr = fmt.Sprintf(" body=%s...", string(bodyData[:60]))
			} else {
				bodyStr = fmt.Sprintf(" body=%s", string(bodyData))
			}
		}

		fmt.Fprintf(os.Stderr, "Frame %s [%s] id=%s: %s%s\n", frameNum, direction, shortID, summary, bodyStr)
		successCount++
	}

	if err := scanner.Err(); err != nil {
		log.Fatal("Scanner error:", err)
	}

	fmt.Fprintf(os.Stderr, "\n--- Summary ---\n")
	fmt.Fprintf(os.Stderr, "Total lines: %d\n", lineNum)
	fmt.Fprintf(os.Stderr, "Success: %d\n", successCount)
	fmt.Fprintf(os.Stderr, "Failed: %d\n", failCount)
}

// ParseEEPROM parses and displays SFP/QSFP EEPROM data from a file
func ParseEEPROM(filename string) {
	data, err := os.ReadFile(filename)
	if err != nil {
		log.Fatalf("Failed to read file: %v", err)
	}

	fmt.Fprintf(os.Stderr, "File: %s (%d bytes)\n\n", filename, len(data))

	// Check for empty/invalid data
	if len(data) == 0 {
		fmt.Fprintln(os.Stderr, "ERROR: File is empty")
		return
	}

	// Check if all 0xff (no module)
	allFF := true
	for _, b := range data {
		if b != 0xff {
			allFF = false
			break
		}
	}
	if allFF {
		fmt.Fprintln(os.Stderr, "WARNING: File contains all 0xFF bytes (no module data)")
		return
	}

	// Determine module type by size and identifier
	if len(data) < 128 {
		fmt.Fprintf(os.Stderr, "ERROR: File too small for SFP EEPROM (need at least 128 bytes, got %d)\n", len(data))
		return
	}

	identifier := data[0]
	switch identifier {
	case 0x03:
		fmt.Fprintln(os.Stderr, "=== SFP/SFP+ Module (SFF-8472) ===")
		fmt.Fprintln(os.Stderr)
		eeprom.ParseSFPDetailed(data)
	case 0x0c:
		fmt.Fprintln(os.Stderr, "=== QSFP Module (SFF-8436) ===")
		fmt.Fprintln(os.Stderr)
		eeprom.ParseQSFPDetailed(data)
	case 0x0d:
		fmt.Fprintln(os.Stderr, "=== QSFP+ Module (SFF-8636) ===")
		fmt.Fprintln(os.Stderr)
		eeprom.ParseQSFPDetailed(data)
	case 0x11:
		fmt.Fprintln(os.Stderr, "=== QSFP28 Module (SFF-8636) ===")
		fmt.Fprintln(os.Stderr)
		eeprom.ParseQSFPDetailed(data)
	default:
		fmt.Fprintf(os.Stderr, "=== Unknown Module Type (identifier: 0x%02X) ===\n\n", identifier)
		// Try SFP parsing anyway
		eeprom.ParseSFPDetailed(data)
	}
}

// RawAPI sends a raw API request and displays the response
func RawAPI(device bluetooth.Device, method, path, body string) {
	ctx := ble.SetupAPI(device)

	// Prepend MAC path if not already present
	fullPath := path
	if !strings.HasPrefix(path, "/api/") {
		fullPath = ctx.APIPath(path)
	}

	fmt.Fprintf(os.Stderr, "Sending %s %s\n", method, fullPath)

	var bodyBytes []byte
	if body != "" {
		bodyBytes = []byte(body)
		fmt.Fprintf(os.Stderr, "Body: %s\n", body)
	}

	resp, respBody, err := ctx.SendRequest(method, fullPath, bodyBytes, 10*time.Second)
	if err != nil {
		log.Fatalf("Request failed: %v", err)
	}

	fmt.Fprintf(os.Stderr, "\nResponse status: %d\n", resp.StatusCode)
	fmt.Fprintf(os.Stderr, "Response body (%d bytes):\n", len(respBody))

	if len(respBody) == 0 {
		fmt.Fprintln(os.Stderr, "  (empty)")
		return
	}

	// Try to pretty-print as JSON
	var prettyJSON map[string]any
	if err := json.Unmarshal(respBody, &prettyJSON); err == nil {
		formatted, _ := json.MarshalIndent(prettyJSON, "  ", "  ")
		fmt.Printf("  %s\n", string(formatted))
	} else if util.IsTextData(respBody) {
		fmt.Printf("  %s\n", string(respBody))
	} else {
		// Binary data - hex dump
		for i := 0; i < len(respBody); i += 16 {
			end := min(i+16, len(respBody))
			fmt.Printf("  %04x: % x\n", i, respBody[i:end])
		}
	}
}

// versionedDB holds a password database with its version info.
type versionedDB struct {
	Version string
	DB      *firmware.PasswordDatabase
}

// PassCompare compares password databases across firmware versions.
// It generates a table showing what passwords would be tried for each module
// under each firmware version's lookup algorithm.
func PassCompare(additionalFiles []string) error {
	var databases []versionedDB

	// Load all downloaded firmware from store
	store, err := firmware.NewFirmwareStore()
	if err != nil {
		return fmt.Errorf("failed to open firmware store: %w", err)
	}

	entries, err := store.List()
	if err != nil {
		return fmt.Errorf("failed to list firmware: %w", err)
	}

	// Load each downloaded firmware
	for _, e := range entries {
		db, err := loadPasswordDB(e.Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to load %s: %v\n", e.Version, err)
			continue
		}
		databases = append(databases, versionedDB{Version: e.Version, DB: db})
	}

	// Load additional files
	for _, path := range additionalFiles {
		db, err := loadPasswordDB(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to load %s: %v\n", path, err)
			continue
		}
		// Extract version from filename
		version := extractVersionFromPath(path)
		databases = append(databases, versionedDB{Version: version, DB: db})
	}

	if len(databases) == 0 {
		fmt.Fprintln(os.Stderr, "No firmware databases loaded.")
		fmt.Fprintln(os.Stderr, "Download firmware with: sfpw fw download")
		fmt.Fprintln(os.Stderr, "Or specify firmware files: sfpw debug pass-compare /path/to/firmware.bin")
		return nil
	}

	// Sort by version
	sort.Slice(databases, func(i, j int) bool {
		return compareVersions(databases[i].Version, databases[j].Version) < 0
	})

	// Collect all unique part numbers across all databases
	partNumbers := collectAllPartNumbers(databases)

	// Print header
	fmt.Printf("# Password Database Comparison\n\n")
	fmt.Printf("Firmware versions loaded: %d\n", len(databases))
	for _, db := range databases {
		fmt.Printf("  - %s: %d entries, %d unique passwords\n",
			db.Version, len(db.DB.Entries), len(db.DB.UniquePasswords()))
	}
	fmt.Printf("\nTotal unique part numbers: %d\n\n", len(partNumbers))

	// Build version headers
	versions := make([]string, len(databases))
	for i, db := range databases {
		versions[i] = db.Version
	}

	// Print table
	fmt.Printf("## Passwords Tried Per Module\n\n")
	fmt.Printf("This table shows what passwords each firmware version would attempt for each module.\n")
	fmt.Printf("Lookup algorithm differences:\n")
	fmt.Printf("- **v1.0.5**: First PN match only (no brute-force, no verification)\n")
	fmt.Printf("- **v1.0.10, v1.1.0**: First PN match OR brute-force all unique if no match (with marker cell verification)\n")
	fmt.Printf("- **v1.1.1**: ALL PN matches OR brute-force if no match (with marker cell verification)\n")
	fmt.Printf("- **v1.1.3+**: ALL PN matches PLUS all unique writable passwords (with marker cell verification)\n\n")

	// Markdown table header
	fmt.Printf("| Part Number |")
	for _, v := range versions {
		fmt.Printf(" %s |", v)
	}
	fmt.Printf("\n|-------------|")
	for range versions {
		fmt.Printf("------|")
	}
	fmt.Printf("\n")

	// Table rows
	for _, pn := range partNumbers {
		fmt.Printf("| %s |", pn)
		for _, db := range databases {
			passwords := getPasswordsForVersion(db.Version, db.DB, pn)
			fmt.Printf(" %s |", passwords)
		}
		fmt.Printf("\n")
	}

	return nil
}

// loadPasswordDB loads a password database from a firmware file.
func loadPasswordDB(path string) (*firmware.PasswordDatabase, error) {
	img, err := firmware.ParseESP32Image(path)
	if err != nil {
		return nil, err
	}
	return firmware.ExtractPasswordDatabase(img)
}

// extractVersionFromPath extracts a version string from a firmware file path.
func extractVersionFromPath(path string) string {
	base := strings.TrimSuffix(path, ".bin")
	parts := strings.Split(base, "/")
	name := parts[len(parts)-1]

	// Strip common prefixes
	for _, prefix := range []string{"sfpw_v", "sfpw_", "ESP32-", "firmware-v", "firmware-", "fw-", "v"} {
		if after, ok := strings.CutPrefix(name, prefix); ok {
			name = after
			break
		}
	}

	return name
}

// compareVersions compares two version strings.
func compareVersions(a, b string) int {
	// Normalize version strings
	a = strings.TrimPrefix(a, "v")
	b = strings.TrimPrefix(b, "v")

	partsA := strings.Split(a, ".")
	partsB := strings.Split(b, ".")

	for i := 0; i < len(partsA) && i < len(partsB); i++ {
		var numA, numB int
		fmt.Sscanf(partsA[i], "%d", &numA)
		fmt.Sscanf(partsB[i], "%d", &numB)
		if numA < numB {
			return -1
		}
		if numA > numB {
			return 1
		}
	}

	return len(partsA) - len(partsB)
}

// collectAllPartNumbers returns all unique part numbers across all databases, sorted.
func collectAllPartNumbers(databases []versionedDB) []string {
	seen := make(map[string]bool)
	for _, db := range databases {
		for _, entry := range db.DB.Entries {
			seen[entry.PartNumber] = true
		}
	}

	result := make([]string, 0, len(seen))
	for pn := range seen {
		result = append(result, pn)
	}
	sort.Strings(result)
	return result
}

// getPasswordsForVersion returns a formatted string of passwords that would be tried
// for a given part number under the specified firmware version's lookup algorithm.
//
// Algorithm analysis from Ghidra reverse engineering across all firmware versions:
//
// v1.0.5 (xsfp_unlock_state_machine - monolithic):
//   - sfp_password_db_lookup_by_partnumber() returns FIRST match only
//   - No verification - just writes password and waits 500ms
//   - If PN not found → fails (no brute force)
//
// v1.0.10, v1.1.0 (xsfp_unlock_idle_handler + FSM handlers):
//   - If PN found: use FIRST match only
//   - If PN NOT found: sfp_collect_unique_passwords() → brute force all unique
//   - Verification via marker cell write test (XOR 0xFF)
//
// v1.1.1 (xsfp_unlock_idle_handler with sfp_collect_entries_by_partnumber):
//   - sfp_collect_entries_by_partnumber() → ALL entries matching PN
//   - If count == 0: brute force via sfp_collect_unique_passwords()
//   - If count >= 1: iterate through ALL matching entries
//   - Verification via marker cell write test
//
// v1.1.3+ (xsfp_state_unlock_enter - full FSM):
//   - ALWAYS: sfp_collect_unique_passwords(list, PN) + sfp_collect_writable_passwords(list)
//   - Tries all PN matches THEN all unique writable passwords
//   - Verification via marker cell write test
func getPasswordsForVersion(version string, db *firmware.PasswordDatabase, partNum string) string {
	// Normalize version for comparison
	v := strings.TrimPrefix(version, "v")

	// Get all entries matching this part number
	matches := db.FindByPartNumber(partNum)

	// Filter out read-only entries for versions that check this
	var writableMatches []firmware.PasswordEntry
	for _, m := range matches {
		if !m.ReadOnly {
			writableMatches = append(writableMatches, m)
		}
	}

	// Determine which passwords would be tried based on version
	var passwordsToTry [][4]byte

	if isVersion105(v) {
		// v1.0.5: First match only, no brute force fallback
		if len(writableMatches) > 0 {
			passwordsToTry = append(passwordsToTry, writableMatches[0].Password)
		}
		// If no match, v1.0.5 fails - no passwords to try
	} else if isVersion113OrLater(v) {
		// v1.1.3+: ALL matching entries + ALL unique writable passwords (always)
		seen := make(map[[4]byte]bool)

		// Phase 1: All entries matching PN
		for _, m := range writableMatches {
			if !seen[m.Password] {
				seen[m.Password] = true
				passwordsToTry = append(passwordsToTry, m.Password)
			}
		}

		// Phase 2: All unique writable passwords from entire database
		for _, pw := range db.UniquePasswords() {
			if !seen[pw] {
				seen[pw] = true
				passwordsToTry = append(passwordsToTry, pw)
			}
		}
	} else if isVersion111(v) {
		// v1.1.1: ALL matching entries, OR brute force if no match
		if len(writableMatches) > 0 {
			// Use ALL matching passwords (deduplicated)
			seen := make(map[[4]byte]bool)
			for _, m := range writableMatches {
				if !seen[m.Password] {
					seen[m.Password] = true
					passwordsToTry = append(passwordsToTry, m.Password)
				}
			}
		} else {
			// No match - brute force all unique passwords
			passwordsToTry = db.UniquePasswords()
		}
	} else {
		// v1.0.10, v1.1.0: FIRST match only, OR brute force if no match
		if len(writableMatches) > 0 {
			// Use first match only
			passwordsToTry = append(passwordsToTry, writableMatches[0].Password)
		} else {
			// No match - brute force all unique passwords
			passwordsToTry = db.UniquePasswords()
		}
	}

	// Format passwords
	if len(passwordsToTry) == 0 {
		return "—"
	}

	var parts []string
	for _, pw := range passwordsToTry {
		parts = append(parts, formatPasswordCompact(pw))
	}
	// Use <br> for line breaks within markdown table cells
	return strings.Join(parts, "<br>")
}

// isVersion105 checks if this is v1.0.5 (simple unlock, no brute force).
func isVersion105(v string) bool {
	return v == "1.0.5" || strings.HasPrefix(v, "1.0.5")
}

// isVersion111 checks if this version is exactly v1.1.1 (ALL PN matches, brute force fallback).
func isVersion111(v string) bool {
	return v == "1.1.1" || strings.HasPrefix(v, "1.1.1")
}

// isVersion113OrLater checks if this version is v1.1.3 or later.
func isVersion113OrLater(v string) bool {
	parts := strings.Split(v, ".")
	if len(parts) < 3 {
		return false
	}
	var major, minor, patch int
	fmt.Sscanf(parts[0], "%d", &major)
	fmt.Sscanf(parts[1], "%d", &minor)
	fmt.Sscanf(parts[2], "%d", &patch)

	if major > 1 {
		return true
	}
	if major == 1 {
		if minor > 1 {
			return true
		}
		if minor == 1 && patch >= 3 {
			return true
		}
	}
	return false
}

// formatPasswordCompact returns a compact hex representation of a password.
func formatPasswordCompact(pw [4]byte) string {
	return fmt.Sprintf("`%02x,%02x,%02x,%02x`", pw[0], pw[1], pw[2], pw[3])
}
