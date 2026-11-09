// Package wslc holds the manual Windows/WSLC integration test. The test file is
// behind the wslc build tag so ordinary Go builds and CI do not require
// wslc.exe or a container session; run it on a Windows device with
// `go test -tags=wslc ./test/wslc`.
package wslc
