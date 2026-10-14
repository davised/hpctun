// Command macapp builds hpctun-tray.app for macOS from thin darwin builds,
// on any platform, and zips it for a release:
//
//	go run ./scripts/macapp -version <ver> -out <zip> \
//	    -hpctun-arm64 <f> -hpctun-amd64 <f> -tray-arm64 <f> -tray-amd64 <f> EXTRA...
//
// Both programs become universal (arm64 and x86_64) like lipo -create
// makes them; EXTRA files go in Contents/.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	var in appInputs
	out := flag.String("out", "", "the zip to write")
	flag.StringVar(&in.Version, "version", "", "the release version, v0.<date>-<commit>")
	flag.StringVar(&in.HPCtun[0], "hpctun-arm64", "", "hpctun built for darwin/arm64")
	flag.StringVar(&in.HPCtun[1], "hpctun-amd64", "", "hpctun built for darwin/amd64")
	flag.StringVar(&in.Tray[0], "tray-arm64", "", "hpctun-tray built for darwin/arm64")
	flag.StringVar(&in.Tray[1], "tray-amd64", "", "hpctun-tray built for darwin/amd64")
	flag.Parse()
	in.Extras = flag.Args()
	if *out == "" || in.Version == "" || in.HPCtun[0] == "" || in.HPCtun[1] == "" || in.Tray[0] == "" || in.Tray[1] == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := writeApp(*out, in); err != nil {
		fmt.Fprintln(os.Stderr, "macapp:", err)
		os.Exit(1)
	}
}
