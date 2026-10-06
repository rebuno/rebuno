package main

import (
	"fmt"
	"os"
)

const (
	bannerBlue        = "\x1b[38;2;6;151;243m"
	bannerBlueShade   = "\x1b[38;2;4;120;195m"
	bannerPurple      = "\x1b[38;2;139;92;246m"
	bannerPurpleShade = "\x1b[38;2;111;72;198m"
	bannerInverse     = "\x1b[7m"
	bannerNormal      = "\x1b[27m"
)

var bannerIcon = []string{
	bannerBlue + "▗▆▆▄▃▁",
	bannerBlue + "▐██████▆▄▂",
	bannerBlue + "▐█████████▇" + bannerBlueShade + "▖",
	bannerBlue + "▐██████████" + bannerBlueShade + "█▖",
	bannerBlue + "▐██████████" + bannerBlueShade + "██▖",
	bannerBlue + "▐██████████" + bannerBlueShade + "███▄",
	bannerBlue + "▐██████████" + bannerBlueShade + "████▙",
	bannerBlue + "▐██████████" + bannerBlueShade + "█████▙",
	bannerBlue + " " + bannerInverse + "▅" + bannerNormal + "▀" + bannerInverse + "▂" + bannerNormal + "███████" + bannerBlueShade + "██████▘",
	bannerPurple + "▐▇▆▄▂" + bannerBlue + "▔" + bannerInverse + "▅" + bannerNormal + "▀" + bannerInverse + "▂" + bannerNormal + "██" + bannerBlueShade + "█" + bannerInverse + "▂" + bannerNormal + "▀" + bannerInverse + "▅" + bannerNormal + "▔" + bannerPurpleShade + "▂▖",
	bannerPurple + "▐█████▇▆▄▃▃" + bannerPurpleShade + "▃▄▆▇██▌",
	bannerPurple + "▝" + bannerInverse + "▃▁" + bannerNormal + "████████" + bannerPurpleShade + "██████▌",
	"    " + bannerPurple + bannerInverse + "▆" + bannerNormal + "▀" + bannerInverse + "▃▁" + bannerNormal + "███" + bannerPurpleShade + "██" + bannerInverse + "▁▃" + bannerNormal + "▀" + bannerInverse + "▆" + bannerNormal,
	"         " + bannerPurple + bannerInverse + "▆▅" + bannerPurpleShade + "▆" + bannerNormal,
}

func printBanner() {
	info, err := os.Stdout.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return
	}
	fmt.Println()
	for _, line := range bannerIcon {
		fmt.Printf("  %s\x1b[0m\n", line)
	}
}
