package main

import (
	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
)

// Build compiles the wt-sbx binary.
func Build() error {
	return sh.RunV("go", "build", "-o", "bin/wt-sbx", "./cmd/wt-sbx")
}

// Install builds and installs wt-sbx.
func Install() error {
	return sh.RunV("go", "install", "./cmd/wt-sbx")
}

type Lint mg.Namespace

// Go runs golangci-lint.
func (Lint) Go() error {
	return sh.RunV("golangci-lint", "run")
}

type Test mg.Namespace

// Unit runs the unit tests.
func (Test) Unit() error {
	return sh.RunV("go", "test", "./...")
}

// Integration runs the integration suite.
func (Test) Integration() error {
	return sh.RunWithV(map[string]string{"WT_SBX_TEST_SBX": "1"},
		"go", "test", "-tags", "integration", "-count=1", "./...")
}
