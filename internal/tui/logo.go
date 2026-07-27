package tui

// logoWidth is the widest rune count across an art's rows.
func logoWidth(art []string) int {
	w := 0
	for _, row := range art {
		if n := len([]rune(row)); n > w {
			w = n
		}
	}
	return w
}
