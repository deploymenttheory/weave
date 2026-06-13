//go:build darwin

package ocr

import (
	"image"
	"testing"
)

func obs(text string, y int) TextObservation {
	return TextObservation{Text: text, Box: image.Rect(0, y, 100, y+20)}
}

// TestFindTextPrefersExact pins that exact matches win over substring matches,
// so "Agree" hits the Agree button (not "Disagree" or license-body text) and
// "English" hits the plain "English" row (not "English (UK)").
func TestFindTextPrefersExact(t *testing.T) {
	terms := []TextObservation{
		obs("BY USING THE APPLE SOFTWARE, YOU ARE AGREEING", 10),
		obs("IF YOU DO NOT AGREE TO THE TERMS", 30),
		obs("Disagree", 900),
		obs("Agree", 905),
	}
	got, ok := FindText("Agree", terms)
	if !ok || got.Text != "Agree" {
		t.Fatalf(`FindText("Agree") = %q, %v; want exact "Agree" button`, got.Text, ok)
	}
	if all := FindAllText("Agree", terms); len(all) != 1 || all[0].Text != "Agree" {
		t.Fatalf(`FindAllText("Agree") = %v; want just the exact "Agree"`, all)
	}

	languages := []TextObservation{
		obs("English (UK)", 10),
		obs("English", 30),
		obs("English (Australia)", 50),
	}
	if got, ok := FindText("English", languages); !ok || got.Text != "English" {
		t.Fatalf(`FindText("English") = %q; want exact "English"`, got.Text)
	}

	// Falls back to substring when there is no exact match.
	if got, ok := FindText("Continue", []TextObservation{obs("Continue Setup", 10)}); !ok || got.Text != "Continue Setup" {
		t.Fatalf(`FindText("Continue") substring fallback = %q, %v`, got.Text, ok)
	}
}
