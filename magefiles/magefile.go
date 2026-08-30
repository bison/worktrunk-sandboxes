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

// All runs every linter.
func (Lint) All() {
	mg.Deps(Lint.Go, Lint.Yaml, Lint.Markdown, Lint.Actions)
}

// Go runs golangci-lint.
func (Lint) Go() error {
	return sh.RunV("golangci-lint", "run")
}

// Yaml runs yamllint.
func (Lint) Yaml() error {
	return sh.RunV("yamllint", ".")
}

// Markdown runs markdownlint.
func (Lint) Markdown() error {
	return sh.RunV("markdownlint", "**/*.md")
}

// Actions runs actionlint.
func (Lint) Actions() error {
	return sh.RunV("actionlint")
}

type Tools mg.Namespace

// Gofumpt formats the entire repo with gofumpt.
func (Tools) Gofumpt() error {
	return sh.RunV("gofumpt", "-l", "-w", ".")
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
