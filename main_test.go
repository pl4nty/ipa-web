package main

import (
	"fmt"
	"testing"
)

// reserved name, called before all tests
func TestMain(t *testing.T) {
	t.Helper()
	initWithCommand(true, false, "text")
	err := login()
	if err != nil {
		print(fmt.Errorf("login failed: %w", err).Error())
	}
}

func TestSearchBundle(t *testing.T) {
	data, err := searchBundles("Apple Pages", 15)
	found := false
	results := []string{}
	if err == nil {
		for _, app := range data.Results {
			if app.BundleID == "com.apple.Pages" {
				found = true
				break
			}
			results = append(results, app.BundleID)
		}
	}

	if !found {
		t.Fatalf(`searchBundle("Apple Pages", 15) returned %q, want BundleID match for %#q, %v`, results, "com.apple.Pages", err)
	}
}

func TestPackageInfo(t *testing.T) {
	data, err := getBundle("com.apple.Pages")
	found := false
	results := []string{}
	if err == nil {
		for _, scheme := range data.Information.CFBundleURLTypes {
			if scheme.CFBundleURLName == "com.apple.iwork.pages-share" {
				found = true
				break
			}
			results = append(results, scheme.CFBundleURLName)
		}
	}

	if !found {
		t.Fatalf(`getPackageInfo("com.apple.Pages") returned %q, want CFBundleURLName match for %#q, %v`, results, "com.apple.iwork.pages-share", err)
	}
}
