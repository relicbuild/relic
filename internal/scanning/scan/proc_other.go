//go:build !unix

package scan

import "os/exec"

func killGroup(*exec.Cmd) {}
