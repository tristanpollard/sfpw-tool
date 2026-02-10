# How to Downgrade Your SFP-Wizard to v1.0.5 (And Why Not To)

## The Short Version

Based on reverse engineering, it appears that firmware v1.0.5 had a bug that may have made it *appear* to successfully write modules that were actually failing silently. Newer firmware appears to correctly detect these failures. The password database has grown with each release.

If this analysis is correct, downgrading won't fix your module - it will just hide the failure.

It appears that:
- The firmware team is significantly ahead of the mobile team on features.
- They're actively working to make the device more likely to be able to write various modules with each version so far.
- The support team is for whatever reason not able to communicate effectively about status and goals.
- As far as I can tell, noone is providing actionable information on the forums (see below).

---

## How to Downgrade

If you want to downgrade anyway (perhaps to test this hypothesis yourself), here's how:

```bash
sfpw-tool fw update /path/to/firmware-1.0.5.bin
```

> [!NOTE]
> You will need to obtain a copy of v1.0.5 firmware yourself. `sfpw-tool` will automatically download and flash any firmware version that is in the upstream repository, but v1.0.5 is not available there.

---

## The Community Misconception

There's a growing sentiment that Ubiquiti has "nerfed" the SFP-Wizard over time - that modules which used to work no longer do after firmware updates.

After reverse engineering firmware versions v1.0.5 through v1.1.3, my analysis suggests this may not be what's actually happening. The evidence points toward a different explanation: older firmware may have been broken in a way that made failures invisible.

---

## What I Found

Using Ghidra and custom tooling, I've analyzed the password unlock system in each firmware version. The SFP-Wizard appears to work by:

1. Reading the module's part number from EEPROM.
2. Looking up that part number in an embedded password database.
3. Sending the corresponding 4-byte password to unlock write access.
4. Writing your changes to the module.

The critical differences between versions are:
- **What happens when unlock fails?** (v1.0.5 doesn't check; v1.0.10+ verifies)
- **What passwords are tried?** (see the comparison table below)

---

## Version-by-Version Analysis

### v1.0.5 — Apparent Silent Failure

Based on disassembly, v1.0.5's unlock state machine appears to:

1. Send the first matching password to the module
2. Wait 500ms
3. Proceed to write (assuming success)

I found no verification step. If this analysis is correct, when the password is wrong, the module remains locked, and subsequent writes silently fail. The user sees "success" but the module is unchanged.

<details>
<summary>Evidence (function addresses)</summary>

| Function | Address |
|----------|---------|
| `xsfp_unlock_state_machine` | `0x4201d8fc` |
| `sfp_password_db_lookup_by_partnumber` | `0x4201b290` |

The monolithic `xsfp_unlock_state_machine` handles all states in a single function. It calls `sfp_password_db_lookup_by_partnumber` which returns the first matching entry. No verification functions exist in this version.

</details>

### v1.0.10, v1.1.0 — Added Verification

Starting with v1.0.10, the firmware added FSM-based unlock with verification:

1. Look up PN in database → use **first match only**
2. If PN not found → brute-force all unique passwords
3. After each password attempt, verify via marker cell test:
   - Read a test byte from the module's EEPROM
   - XOR it with 0xFF (flip all bits)
   - Attempt to write the modified value back
   - If write succeeds → unlocked, restore original byte
   - If write fails → try the next password, or report failure

<details>
<summary>Evidence (function addresses)</summary>

**v1.0.10:**
| Function | Address |
|----------|---------|
| `xsfp_unlock_idle_handler` | `0x42021690` |
| `xsfp_unlock_verify_handler` | `0x42021120` |
| `xsfp_unlock_next_password_handler` | `0x4202123c` |
| `sfp_collect_unique_passwords` | `0x4201c9fc` |

**v1.1.0:**
| Function | Address |
|----------|---------|
| `xsfp_unlock_idle_handler` | `0x4202303c` |
| `xsfp_unlock_verify_handler` | `0x42022acc` |
| `xsfp_unlock_next_password_handler` | `0x42022be8` |
| `sfp_collect_unique_passwords` | `0x4201d4f8` |

The `xsfp_unlock_idle_handler` checks if PN is found; if not, calls `sfp_collect_unique_passwords` for brute-force. The `xsfp_unlock_verify_handler` performs the marker cell XOR test.

</details>

### v1.1.1 — Multiple Match Support

v1.1.1 changed the password selection when a PN is found in the database:

1. Look up PN → use **all matching entries** (not just first)
2. If PN not found → brute-force all unique passwords
3. Same marker cell verification as v1.0.10

This handles cases where a module has multiple valid passwords in the database.

<details>
<summary>Evidence (function addresses)</summary>

| Function | Address |
|----------|---------|
| `xsfp_unlock_idle_handler` | `0x42023224` |
| `xsfp_unlock_verify_handler` | `0x42022cb4` |
| `sfp_collect_entries_by_partnumber` | `0x4201d5f4` |
| `sfp_collect_unique_passwords` | `0x4201d634` |

The key change is the new `sfp_collect_entries_by_partnumber` function (2 params) which collects ALL entries matching a PN, not just the first. The `xsfp_unlock_idle_handler` was rewritten to use this function.

</details>

### v1.1.3 — Comprehensive Brute-Force

The current release tries **all** known passwords for every module, regardless of whether there's a database match:

1. Collect all entries matching PN
2. Then collect all unique writable passwords from entire database
3. Try each in sequence with marker cell verification

This maximizes compatibility but takes longer.

<details>
<summary>Evidence (function addresses)</summary>

| Function | Address |
|----------|---------|
| `xsfp_unlock_fsm_run` | `0x42019d60` |
| `xsfp_state_unlock_enter` | `0x42019e54` |
| `xsfp_verify_unlock_entry` | `0x42019a4c` |
| `sfp_collect_unique_passwords` | `0x420158ec` |
| `sfp_collect_writable_passwords` | `0x42015934` |

The FSM was restructured. `xsfp_state_unlock_enter` now calls BOTH `sfp_collect_unique_passwords(list, part_number)` (note: 2 params, filters by PN) AND `sfp_collect_writable_passwords(list)` which adds all remaining unique writable passwords.

</details>

---

# Password Database Comparison

Firmware versions loaded: 5
  - 1.0.5: 54 entries, 5 unique passwords
  - v1.0.10: 54 entries, 5 unique passwords
  - v1.1.0: 54 entries, 5 unique passwords
  - v1.1.1: 58 entries, 6 unique passwords
  - v1.1.3: 59 entries, 6 unique passwords

Total unique part numbers: 55

## Passwords Tried Per Module

This table shows what passwords each firmware version would attempt for each module.
Lookup algorithm differences:
- **v1.0.5**: First PN match only (no brute-force, no verification)
- **v1.0.10, v1.1.0**: First PN match OR brute-force all unique if no match (with marker cell verification)
- **v1.1.1**: ALL PN matches OR brute-force if no match (with marker cell verification)
- **v1.1.3+**: ALL PN matches PLUS all unique writable passwords (with marker cell verification)

| Part Number | 1.0.5 | v1.0.10 | v1.1.0 | v1.1.1 | v1.1.3 |
|-------------|------|------|------|------|------|
| AOC-QSFP28-10M | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| AOC-QSFP28-20M | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| AOC-QSFP28-30M | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| AOC-QSFP28-5M | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| AOC-SFP10-10M | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| AOC-SFP10-20M | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| AOC-SFP10-30M | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| AOC-SFP10-5M | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| AOC-SFP28-10M | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| AOC-SFP28-20M | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| AOC-SFP28-30M | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| AOC-SFP28-5M | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| CM-RJ45-1G | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff`<br>`00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| DAC-QSFP28-0.5M | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff`<br>`00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| DAC-QSFP28-1M | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff`<br>`00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| DAC-QSFP28-3M | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff`<br>`00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| DAC-QSFP28-5M | — | `00,00,10,11`<br>`78,56,34,12`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` | `00,00,10,11`<br>`78,56,34,12`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` | `00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` | `ff,ff,ff,ff`<br>`00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| DAC-SFP10-0.5M | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff`<br>`00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| DAC-SFP10-1M | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff`<br>`00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| DAC-SFP10-3M | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff`<br>`00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| DAC-SFP28-0.5M | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff`<br>`00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| DAC-SFP28-1M | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff`<br>`00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| DAC-SFP28-3M | `78,56,34,12` | `78,56,34,12` | `78,56,34,12` | `78,56,34,12` | `78,56,34,12`<br>`00,00,10,11`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| DAC-SFP28-5M | — | `00,00,10,11`<br>`78,56,34,12`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` | `00,00,10,11`<br>`78,56,34,12`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` | `00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` | `00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| OM-MM-10G-D | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11`<br>`63,73,77,77` | `00,00,10,11`<br>`63,73,77,77`<br>`78,56,34,12`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| OM-MM-1G-D | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| OM-QSFP28-LR4 | `80,81,82,83` | `80,81,82,83` | `80,81,82,83` | `80,81,82,83` | `80,81,82,83`<br>`00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`51,53,46,50` |
| OM-QSFP28-PSM4 | `80,81,82,83` | `80,81,82,83` | `80,81,82,83` | `80,81,82,83` | `80,81,82,83`<br>`00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`51,53,46,50` |
| OM-QSFP28-SR4 | `51,53,46,50` | `51,53,46,50` | `51,53,46,50` | `51,53,46,50` | `51,53,46,50`<br>`00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83` |
| OM-SFP10-1270 | `78,56,34,12` | `78,56,34,12` | `78,56,34,12` | `78,56,34,12` | `78,56,34,12`<br>`00,00,10,11`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| OM-SFP10-1290 | `78,56,34,12` | `78,56,34,12` | `78,56,34,12` | `78,56,34,12` | `78,56,34,12`<br>`00,00,10,11`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| OM-SFP10-1310 | `78,56,34,12` | `78,56,34,12` | `78,56,34,12` | `78,56,34,12` | `78,56,34,12`<br>`00,00,10,11`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| OM-SFP10-1330 | `78,56,34,12` | `78,56,34,12` | `78,56,34,12` | `78,56,34,12` | `78,56,34,12`<br>`00,00,10,11`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| OM-SFP10-1450 | `78,56,34,12` | `78,56,34,12` | `78,56,34,12` | `78,56,34,12` | `78,56,34,12`<br>`00,00,10,11`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| OM-SFP10-1470 | `78,56,34,12` | `78,56,34,12` | `78,56,34,12` | `78,56,34,12` | `78,56,34,12`<br>`00,00,10,11`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| OM-SFP10-1490 | `78,56,34,12` | `78,56,34,12` | `78,56,34,12` | `78,56,34,12` | `78,56,34,12`<br>`00,00,10,11`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| OM-SFP10-1510 | `78,56,34,12` | `78,56,34,12` | `78,56,34,12` | `78,56,34,12` | `78,56,34,12`<br>`00,00,10,11`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| OM-SFP10-1530 | `78,56,34,12` | `78,56,34,12` | `78,56,34,12` | `78,56,34,12` | `78,56,34,12`<br>`00,00,10,11`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| OM-SFP10-1550 | `78,56,34,12` | `78,56,34,12` | `78,56,34,12` | `78,56,34,12` | `78,56,34,12`<br>`00,00,10,11`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| OM-SFP10-1570 | `78,56,34,12` | `78,56,34,12` | `78,56,34,12` | `78,56,34,12` | `78,56,34,12`<br>`00,00,10,11`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| OM-SFP10-1590 | `78,56,34,12` | `78,56,34,12` | `78,56,34,12` | `78,56,34,12` | `78,56,34,12`<br>`00,00,10,11`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| OM-SFP28-LR | `53,46,50,58` | `53,46,50,58` | `53,46,50,58` | `53,46,50,58`<br>`63,73,77,77` | `53,46,50,58`<br>`63,73,77,77`<br>`00,00,10,11`<br>`78,56,34,12`<br>`80,81,82,83`<br>`51,53,46,50` |
| OM-SFP28-SR | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11`<br>`63,73,77,77` | `00,00,10,11`<br>`63,73,77,77`<br>`78,56,34,12`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| OM-SM-10G-D | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `63,73,77,77`<br>`00,00,10,11` | `63,73,77,77`<br>`00,00,10,11`<br>`78,56,34,12`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| OM-SM-10G-S | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| OM-SM-1G-S | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| UACC-CM-RJ45-MG | — | `00,00,10,11`<br>`78,56,34,12`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` | `00,00,10,11`<br>`78,56,34,12`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` | `00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` | `00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| UACC-UF-OM-XGS | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| UC-D-QSFP28-0.5M | — | `00,00,10,11`<br>`78,56,34,12`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` | `00,00,10,11`<br>`78,56,34,12`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` | `00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` | `00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| UC-D-QSFP28-1M | — | `00,00,10,11`<br>`78,56,34,12`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` | `00,00,10,11`<br>`78,56,34,12`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` | `00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` | `00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| UC-D-QSFP28-3M | — | `00,00,10,11`<br>`78,56,34,12`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` | `00,00,10,11`<br>`78,56,34,12`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` | `00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` | `00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| Uplink-SFP28-.15 | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff`<br>`00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| Uplink-SFP28-0.3 | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff` | `ff,ff,ff,ff`<br>`00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| Uplink-SFP28-30M | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |
| Uplink-SFP28-3M | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11` | `00,00,10,11`<br>`78,56,34,12`<br>`63,73,77,77`<br>`53,46,50,58`<br>`80,81,82,83`<br>`51,53,46,50` |

---

## What About Unlisted Modules?

If a module's part number isn't in the database:

- **v1.0.5**: No passwords are tried
- **v1.0.10 through v1.1.1**: All known passwords are brute-forced
- **v1.1.3**: All known passwords are always tried regardless

---

## Why Downgrading Probably Won't Help

Based on my analysis, if you downgrade to v1.0.5:

1. **Your module likely still won't unlock** — the password is either correct or it isn't
2. **Failures may be hidden** — the firmware may report success even when writes fail
4. **You lose brute-force fallback** — v1.0.5 only tries one password per module

> [!IMPORTANT]
> The one scenario where v1.0.5 might legitimately work better is if the unlock verification in v1.0.10+ has a bug that incorrectly rejects successful unlocks. This is theoretically possible but would need to be proven with a specific module that:
> - Successfully unlocks in v1.0.5 (verified by actually reading back written changes)
> - Fails to unlock in v1.1.3 despite using the same password
>
> If you have such a module, please share your findings.

---

## How You Can Actually Help

If you have a module that won't write, likely the most useful thing you can do is gather and share accurate information on the forums. The device firmware is clearly being actively developed in the area of module compatibility.

### 1. Get the Exact Part Number

The firmware uses the **part number string** from the module's EEPROM (bytes 40-55 of page A0h) for lookup. This must match exactly—including spaces, capitalization, and revision suffixes.

```bash
sfpw-tool module info | jq -r .partNumber
```

### 2. Research the Password

Search forums, GitHub, and vendor documentation for your specific module. Communities have figured out passwords for various vendors—FS.com, Cisco/Finisar, generic Chinese OEM modules, etc.

### 3. Report with Details

When reporting a non-working module, include:

- `sfpw-tool module info` output
- Firmware version you tested
- What you tried and what happened
- Any known working password (if you found one online)

### 4. Test the Hypothesis

If you have access to v1.0.5 firmware and a module that "worked" before:

1. Downgrade to v1.0.5
2. Write a change to the module
3. **Actually verify the write worked** by reading the module back and confirming your changes persisted. You can verify this pretty thoroughly with `sfpw-tool module read output.bin && sfpw-tool debug parse-eeprom output.bin` before and after writing.
4. Report your findings either way

This would help confirm or refute the silent failure hypothesis.

---

## Further Reading

- [API.md](../API.md) — Protocol documentation including password database internals
- [sfpw-tool](../) — CLI tool for SFP-Wizard interaction and firmware analysis

---

*This document is based on static reverse engineering of firmware versions 1.0.5, 1.0.10, 1.1.0, 1.1.1, and 1.1.3 using Ghidra. No physical testing with affected modules was performed. The analysis may contain errors. Corrections and real-world test results are welcome.*
