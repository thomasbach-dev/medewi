package backup

import (
	"fmt"
	"sort"
)

var layout32 = Layout{Firmware: "3.2", Entries: sorted(
	[]Entry{
		{0x0132, "u16", 0.1, 0, "PID 1 steam beep setpoint (°)", "PID 1/Steam beep/Setpoint", true, "likely", "equals MCu, unchanged"},
		{0x0134, "u16", 0.1, 0, "PID 1 eco setpoint (°)", "PID 1/Eco", true, "likely", "equals MCu, unchanged"},
		{0x0136, "u16", 0.1, 0, "PID 1 reset setpoint (°)", "PID 1/Reset", true, "likely", "equals MCu, unchanged"},
		{0x0138, "u16", 0.1, 0, "PID 1 user setpoint 1 (°)", "PID 1/SP User 1", true, "confirmed", "- -> 92.2 -> -"},
		{0x013A, "u16", 0.1, 0, "PID 1 user setpoint 2 (°)", "PID 1/SP User 2", true, "likely", "next to user setpoint 1, unset"},
		{0x013C, "u16", 0.1, 0, "PID 1 user setpoint 3 (°)", "PID 1/SP User 3", true, "likely", "next to user setpoint 2, unset"},
		{0x013E, "u16", 0.1, 0, "PID 1 safety limit (°)", "PID 1/Safety", true, "likely", "equals MCu, unchanged"},
		{0x0173, "u16", 0.1, 0, "PID 1 Kc (%/°)", "PID 1/Kc", true, "confirmed", "matches MCu 8.9 -> 8.8"},
		{0x0175, "u16", 0.1, 0, "PID 1 Ti (s)", "PID 1/Ti", true, "confirmed", "67.2 -> 72.3"},
		{0x0177, "u16", 0.1, 0, "PID 1 Td (s)", "PID 1/Td", true, "confirmed", "16.8 -> 18.1"},
		{0x0179, "u16", 0.1, 0, "PID 1 windup limit (%)", "PID 1/WU limit", true, "likely", "200 = MCu 20.0 %, unchanged"},
		{0x04F9, "u8", 1, 0, "Profiles: Use (slot)", "Profiles/Use", false, "likely", "1 -> 3, MCu SOAK 1.0b -> DARK"},
		{0x08AA, "u16", 1, 0, "Eco: time to eco (min)", "Eco/Eco", true, "confirmed", "45 -> 46 -> 45"},
		{0x08AC, "u16", 1, 0, "Eco: time to standby (min)", "Eco/Standby", true, "confirmed", "90 -> 91 -> 90"},
	},
	profileEntries32(),
	doseEntries32(),
), State: state32}

// state32 is the block of counters and current setpoints. It was seen at
// 0x0021 (until 2026-11-21) and at 0x003F (2026-11-27), after its second
// byte had wrapped: it seems to move by its own length every 256 saves.
// The region runs from the image's three header bytes to the bytes before
// the checksum; where the block goes after 0x005D is not known.
var state32 = Block{From: 0x0003, To: 0x0083, Size: 0x1E, Entries: []Entry{
	{0x01, "u8", 1, 0, "run counter?", "", false, "guess", "+5 = runs 5-9; the block moved after it wrapped"},
	{0x02, "u16", 0.1, 0, "PID 1 setpoint (°)", "PID 1/Setpoint", true, "likely", "equals MCu in every backup, unchanged"},
	{0x04, "u16", 0.1, 0, "PID 2 setpoint (°)?", "PID 2/Setpoint", true, "likely", "equals MCu in every backup, unchanged"},
	{0x06, "u24", 1, 0, "flow-meter ticks, lifetime?", "", false, "likely", "+1006 = sum of field_31 over runs 5-9"},
	{0x0E, "u24", 1, 0, "flow-meter ticks, copy or trip", "", false, "likely", "same values as the lifetime ticks"},
	{0x12, "u16", 1, 0, "flow-meter ticks, resettable?", "", false, "likely", "+1006"},
	{0x16, "u8", 1, 0, "shot counter?", "", false, "guess", "+1 = one shot"},
}}

// Pressure profiles: four slots of profileSize32 bytes. A slot starts
// with its 9-character name (right-aligned, space-padded; a cleared name
// is nine spaces, a never-used one nine zero bytes).
const (
	profileBase32 = 0x04FF
	profileSize32 = 0x6B
	profileSlots  = 4
	profilePoints = 9
)

// profileEntries32 lists the entries of the four profile slots. Slot 4
// was edited on 2026-11-27 (confirmed); the other slots have the same
// structure and their values equal the MCu (likely).
func profileEntries32() []Entry {
	var out []Entry
	for slot := 1; slot <= profileSlots; slot++ {
		base := profileBase32 + (slot-1)*profileSize32
		path := fmt.Sprintf("Profiles/Edit/%d/", slot)
		name := fmt.Sprintf("Profile %d ", slot)
		conf := func(edited bool, evidence string) (string, string) {
			if slot == 4 && edited {
				return "confirmed", evidence
			}
			return "likely", "same structure as slot 4, equals MCu"
		}
		for _, part := range []struct {
			menu, label string
			off, edited int // edited: how many points were changed in slot 4
		}{{"Edit PI...", "PI", 0x0D, 1}, {"Edit shot...", "shot", 0x35, 3}} {
			for pt := 1; pt <= profilePoints; pt++ {
				off := base + part.off + 4*(pt-1)
				c, ev := conf(pt <= part.edited, "- -> set -> -; raw = tenths of a second + 1")
				out = append(out, Entry{
					off, "u16", 0.1, 1, fmt.Sprintf("%s%s point %d time (s)", name, part.label, pt),
					fmt.Sprintf("%s%s/%d/Time", path, part.menu, pt), true, c, ev,
				})
				c, ev = conf(pt <= part.edited, "- -> set -> -; raw = tenths of a bar + 181")
				out = append(out, Entry{
					off + 2, "u16", 0.1, 181, fmt.Sprintf("%s%s point %d pressure (bar)", name, part.label, pt),
					fmt.Sprintf("%s%s/%d/Pump", path, part.menu, pt), true, c, ev,
				})
			}
		}
		c, ev := conf(true, "- -> 5 -> -")
		out = append(out, Entry{base + 0x5D, "u8", 1, 0, name + "PI repeat", path + "Edit PI.../Repeat", true, c, ev})
		c, ev = conf(true, "- -> 4.4 -> -")
		out = append(out, Entry{base + 0x5E, "u8", 0.1, 0, name + "PI wait (s)", path + "Edit PI.../Wait", true, c, ev})
		out = append(out, Entry{
			base + 0x60, "u8", 1, 0, name + "end (0 STOP, 1 SUSTAIN)", path + "End", false,
			"likely", "1 in the slots that say SUSTAIN, 0 in the one that says STOP",
		})
		c, ev = conf(true, "- -> 93.3 -> -")
		out = append(out, Entry{base + 0x67, "u16", 0.1, 0, name + "PID 1 setpoint (°)", path + "PID 1", true, c, ev})
		out = append(out, Entry{
			base + 0x69, "u16", 0.1, 0, name + "PID 2 setpoint (°)?", path + "PID 2", false,
			"guess", "last two bytes of the slot, after PID 1; unset",
		})
	}
	return out
}

// Doses: eleven records of doseSize32 bytes in menu order. Which fields
// the MCu shows depends on the dose's method (Volume, or In/Out/Ratio).
const (
	doseBase32 = 0x079E
	doseSize32 = 0x11
)

// doseNames32 are the MCu labels; "Cococa" is the firmware's spelling.
var doseNames32 = []string{
	"Ristretto", "Espresso", "Lungo", "Dbl Ristretto", "Dbl Espresso", "Cafe Creme",
	"Allonge", "Cococa", "Tea", "Americano", "Americano XL",
}

func doseEntries32() []Entry {
	var out []Entry
	for i, dose := range doseNames32 {
		base := doseBase32 + i*doseSize32
		path := "Doses/" + dose + "/"
		conf, ev := "likely", "same structure as Lungo, equals MCu where the method shows it"
		switch dose {
		case "Lungo":
			conf, ev = "confirmed", "110 -> 123 -> 110"
		case "Dbl Espresso":
			conf, ev = "confirmed", "changed together with the MCu on 2026-11-21"
		}
		const byWeight = "equals MCu in every dose by weight"
		out = append(out,
			Entry{base + 2, "u16", 1, 0, dose + " volume (ml)", path + "Volume", true, conf, ev},
			Entry{base + 4, "u16", 0.1, 0, dose + " in (g)", path + "In", true, "likely", byWeight},
			Entry{base + 6, "u16", 0.1, 0, dose + " out (g)", path + "Out", true, "likely", byWeight},
			Entry{base + 8, "u16", 0.1, 0, dose + " ratio 1:x", path + "Ratio 1 :", true, "likely", byWeight},
		)
	}
	return out
}

// sorted joins entry lists and orders them by offset.
func sorted(lists ...[]Entry) []Entry {
	var out []Entry
	for _, l := range lists {
		out = append(out, l...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Offset < out[j].Offset })
	return out
}
