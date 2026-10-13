module Kizuna-Eye

go 1.27.1

// toolchain pins the exact patch used to build the host AND every plugin.
// Go plugins require an exact build-hash match for shared packages, and a
// different toolchain patch produces a different hash: a host rebuilt with
// go1.27.2 refuses a plugin built with go1.27.1 with
// "plugin was built with a different version of package". Pinning here (with
// GOTOOLCHAIN=auto) makes `go build` fetch and use this exact version, so
// host and plugins stay ABI-compatible. go1.27.2 also fixes ten reachable
// net/http HTTP/2 vulnerabilities found by govulncheck in go1.27.1.
toolchain go1.27.2

require (
	github.com/gorilla/websocket v1.5.3
	github.com/mouuff/go-rocket-update v1.5.6
	github.com/shirou/gopsutil/v3 v3.24.5
	golang.org/x/crypto v0.57.0
)

require (
	github.com/go-ole/go-ole v1.2.6 // indirect
	github.com/lufia/plan9stats v0.0.0-20211012122336-39d0f177ccd0 // indirect
	github.com/power-devops/perfstat v0.0.0-20210106213030-5aafc221ea8c // indirect
	github.com/shoenig/go-m1cpu v0.1.6 // indirect
	github.com/tklauser/go-sysconf v0.3.12 // indirect
	github.com/tklauser/numcpus v0.6.1 // indirect
	github.com/yusufpapurcu/wmi v1.2.4 // indirect
	golang.org/x/sys v0.48.0 // indirect
)
