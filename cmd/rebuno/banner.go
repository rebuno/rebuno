package main

import (
	"fmt"
	"os"
)

var bannerWordmark = []string{
	"                     ██",
	"████▄████  ▄█████▄   ██▄████▄   ██    ██  ██▄████▄   ▄█████▄",
	"   ██     ██▀   ▀██  ██▀   ▀██  ██    ██  ██    ██  ██▀   ▀██",
	"   ██     █████████  ██     ██  ██    ██  ██    ██  ██     ██",
	"   ██     ██▄   ▄▄▄  ██▄   ▄██  ██    ██  ██    ██  ██▄   ▄██",
	"████████   ▀█████▀   ██▀████▀   ▀████▀██  ██    ██   ▀█████▀",
}

func printBanner() {
	info, err := os.Stdout.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return
	}
	fmt.Println()
	for _, line := range bannerWordmark {
		fmt.Printf("  %s\n", line)
	}
}
