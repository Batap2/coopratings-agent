package main

// Windows resources for the exe (icon, name, version, manifest):
//
//	go generate
//
// writes rsrc_windows_amd64.syso, which "go build" embeds automatically.
// assets/icon.ico is drawn by the program itself (--write-icon).

//go:generate go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@v1.7.0 -64 -icon=assets/icon.ico -o rsrc_windows_amd64.syso versioninfo.json
