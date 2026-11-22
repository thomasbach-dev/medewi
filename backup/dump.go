package backup

// CheckDump checks the reply to a dump command: for MCu and MCm it returns
// the Problems of ParseMCu / ParseMCm (an empty reply is one), for any
// other command none. A reply with problems was cut short or lost lines;
// the machine may still be sending it.
func CheckDump(cmd string, lines []string) []Problem {
	switch cmd {
	case "MCu":
		return ParseMCu(lines).Problems
	case "MCm":
		return ParseMCm(lines).Problems
	}
	return nil
}
