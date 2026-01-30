package commands

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/vitaminmoo/sfpw-tool/internal/ble"
	"github.com/vitaminmoo/sfpw-tool/internal/config"

	"tinygo.org/x/bluetooth"
)

// FirmwareStatus represents the response from GET /fw
type FirmwareStatus struct {
	HWVersion       int    `json:"hwv"`
	FWVersion       string `json:"fwv"`
	IsUpdating      bool   `json:"isUPdating"` // Note: typo in API
	Status          string `json:"status"`
	ProgressPercent int    `json:"progressPercent"`
	RemainingTime   int    `json:"remainingTime"`
}

// FirmwareStartResponse represents the response from POST /fw/start
type FirmwareStartResponse struct {
	Status string `json:"status"`
	Offset int    `json:"offset"`
	Chunk  int    `json:"chunk"`
	Size   int    `json:"size"`
}

// FirmwareUpdate uploads and installs new firmware
func FirmwareUpdate(device bluetooth.Device, filename string) {
	ctx := ble.SetupAPI(device)

	// Read the firmware file
	fwData, err := os.ReadFile(filename)
	if err != nil {
		log.Fatalf("Failed to read firmware file: %v", err)
	}

	fmt.Fprintf(os.Stderr, "Loaded firmware file: %d bytes from %s\n", len(fwData), filename)

	// Get current firmware status
	fmt.Fprintln(os.Stderr, "Checking current firmware status...")
	status, err := getFirmwareStatus(ctx)
	if err != nil {
		log.Fatalf("Failed to get firmware status: %v", err)
	}

	fmt.Fprintf(os.Stderr, "Current firmware: v%s (hw: %d)\n", status.FWVersion, status.HWVersion)
	fmt.Fprintf(os.Stderr, "Update status: %s\n", status.Status)

	if status.IsUpdating {
		fmt.Fprintln(os.Stderr, "WARNING: A firmware update is already in progress!")
		if !ConfirmAction("Abort existing update and start new one? (yes/no): ") {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return
		}

		// Abort existing update
		fmt.Fprintln(os.Stderr, "Aborting existing update...")
		if err := abortFirmwareUpdate(ctx); err != nil {
			log.Fatalf("Failed to abort existing update: %v", err)
		}
		time.Sleep(1 * time.Second)
	}

	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "WARNING: Firmware update is a potentially dangerous operation!")
	fmt.Fprintln(os.Stderr, "Do not disconnect power or BLE during the update.")
	fmt.Fprintf(os.Stderr, "File size: %d bytes\n", len(fwData))
	if !ConfirmAction("Type 'yes' to start firmware update: ") {
		fmt.Fprintln(os.Stderr, "Aborted.")
		return
	}

	// Step 1: Start firmware update
	fmt.Fprintln(os.Stderr, "\nStarting firmware update...")
	startResp, err := startFirmwareUpdate(ctx, len(fwData))
	if err != nil {
		log.Fatalf("Failed to start firmware update: %v", err)
	}
	config.Debugf("Start response: %+v", startResp)

	// Determine chunk size (use what device tells us, or default)
	chunkSize := 512 // default
	if startResp.Chunk > 0 {
		chunkSize = startResp.Chunk
	}
	config.Debugf("Using chunk size: %d bytes", chunkSize)

	// Step 2: Send firmware data in chunks
	fmt.Fprintf(os.Stderr, "Uploading firmware (%d bytes in %d-byte chunks)...\n", len(fwData), chunkSize)

	totalChunks := (len(fwData) + chunkSize - 1) / chunkSize
	for offset := 0; offset < len(fwData); offset += chunkSize {
		end := offset + chunkSize
		if end > len(fwData) {
			end = len(fwData)
		}
		chunk := fwData[offset:end]
		chunkNum := (offset / chunkSize) + 1

		// Send chunk
		err := sendFirmwareChunk(ctx, chunk, offset)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\nFailed to send chunk at offset %d: %v\n", offset, err)
			fmt.Fprintln(os.Stderr, "Aborting firmware update...")
			abortFirmwareUpdate(ctx)
			return
		}

		// Progress bar
		progress := float64(offset+len(chunk)) / float64(len(fwData)) * 100
		fmt.Fprintf(os.Stderr, "\r  Chunk %d/%d: %d-%d bytes (%.1f%%)", chunkNum, totalChunks, offset, end, progress)

		// Small delay between chunks
		time.Sleep(20 * time.Millisecond)
	}
	fmt.Fprintln(os.Stderr)

	// Step 3: Monitor update progress
	fmt.Fprintln(os.Stderr, "Firmware uploaded. Monitoring installation progress...")

	for {
		time.Sleep(2 * time.Second)

		status, err := getFirmwareStatus(ctx)
		if err != nil {
			// Connection may drop during update - that's often expected
			fmt.Fprintf(os.Stderr, "Status check failed (connection may have dropped): %v\n", err)
			fmt.Fprintln(os.Stderr, "The device may be rebooting. Please check device status manually.")
			return
		}

		fmt.Fprintf(os.Stderr, "\r  Status: %s, Progress: %d%%, Remaining: %ds    ",
			status.Status, status.ProgressPercent, status.RemainingTime)

		if !status.IsUpdating {
			fmt.Fprintln(os.Stderr)
			if status.Status == "finished" || status.Status == "complete" {
				fmt.Fprintf(os.Stderr, "Firmware update complete! New version: v%s\n", status.FWVersion)
			} else {
				fmt.Fprintf(os.Stderr, "Update finished with status: %s\n", status.Status)
			}
			break
		}
	}
}

// startFirmwareUpdate initiates a firmware update
func startFirmwareUpdate(ctx *ble.APIContext, size int) (*FirmwareStartResponse, error) {
	// Send size as JSON body
	reqBody := fmt.Sprintf(`{"size":%d}`, size)

	resp, body, err := ctx.SendRequest("POST", ctx.APIPath("/fw/start"), []byte(reqBody), 10*time.Second)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var startResp FirmwareStartResponse
	if err := json.Unmarshal(body, &startResp); err != nil {
		// Return with defaults if we can't parse
		config.Debugf("Could not parse start response: %v, body: %s", err, string(body))
		return &FirmwareStartResponse{Status: "ready"}, nil
	}

	return &startResp, nil
}

// sendFirmwareChunk sends a chunk of firmware data
func sendFirmwareChunk(ctx *ble.APIContext, chunk []byte, offset int) error {
	resp, body, err := ctx.SendRawBodyRequest("POST", ctx.APIPath("/fw/data"), chunk, 30*time.Second)
	if err != nil {
		return err
	}

	if resp.StatusCode != 200 {
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	config.Debugf("Chunk at offset %d sent successfully", offset)
	return nil
}

// getFirmwareStatus gets the current firmware update status
func getFirmwareStatus(ctx *ble.APIContext) (*FirmwareStatus, error) {
	resp, body, err := ctx.SendRequest("GET", ctx.APIPath("/fw"), nil, 10*time.Second)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var status FirmwareStatus
	if err := json.Unmarshal(body, &status); err != nil {
		return nil, fmt.Errorf("failed to parse status: %w", err)
	}

	return &status, nil
}

// abortFirmwareUpdate aborts an in-progress firmware update
func abortFirmwareUpdate(ctx *ble.APIContext) error {
	resp, body, err := ctx.SendRequest("POST", ctx.APIPath("/fw/abort"), nil, 10*time.Second)
	if err != nil {
		return err
	}

	if resp.StatusCode != 200 {
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// FirmwareAbort aborts an in-progress firmware update
func FirmwareAbort(device bluetooth.Device) {
	ctx := ble.SetupAPI(device)

	fmt.Fprintln(os.Stderr, "Checking firmware status...")
	status, err := getFirmwareStatus(ctx)
	if err != nil {
		log.Fatalf("Failed to get firmware status: %v", err)
	}

	if !status.IsUpdating {
		fmt.Fprintln(os.Stderr, "No firmware update in progress.")
		return
	}

	fmt.Fprintf(os.Stderr, "Update in progress: %d%% complete, status: %s\n", status.ProgressPercent, status.Status)
	if !ConfirmAction("Abort update? (yes/no): ") {
		fmt.Fprintln(os.Stderr, "Cancelled.")
		return
	}

	fmt.Fprintln(os.Stderr, "Aborting firmware update...")
	if err := abortFirmwareUpdate(ctx); err != nil {
		log.Fatalf("Failed to abort: %v", err)
	}

	fmt.Fprintln(os.Stderr, "Firmware update aborted.")
}

// FirmwareStatusCmd shows detailed firmware status
func FirmwareStatusCmd(device bluetooth.Device) {
	ctx := ble.SetupAPI(device)

	resp, body, err := ctx.SendRequest("GET", ctx.APIPath("/fw"), nil, 10*time.Second)
	if err != nil {
		log.Fatal("API request failed:", err)
	}

	if resp.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "Error: status %d\n", resp.StatusCode)
		fmt.Fprintf(os.Stderr, "Body: %s\n", string(body))
		return
	}

	PrintJSON(body)
}
