package main

import "testing"

// The ciphertext below was produced by the web app's encryptKey
// (web-client/src/lib/openrouter-key.ts) with KEY_ENCRYPTION_SECRET=topsecret,
// so this pins the two implementations to the same format.
func TestDecryptAPIKeyMatchesWebApp(t *testing.T) {
	t.Setenv("KEY_ENCRYPTION_SECRET", "topsecret")
	got, err := decryptAPIKey("Cdue4YBNwu8pEdtTYx1CRMNsFQZRPzI2ULu358BrbbgOFBDukPbi2Gb5LDykb0oFwA==")
	if err != nil {
		t.Fatal(err)
	}
	if got != "sk-or-v1-abcdef123456" {
		t.Fatalf("got %q", got)
	}

	t.Setenv("KEY_ENCRYPTION_SECRET", "wrong")
	if _, err := decryptAPIKey("Cdue4YBNwu8pEdtTYx1CRMNsFQZRPzI2ULu358BrbbgOFBDukPbi2Gb5LDykb0oFwA=="); err == nil {
		t.Fatal("decrypting with the wrong secret must fail")
	}
}

func TestCleanDatabaseURL(t *testing.T) {
	want := "postgresql://u:p@ep-x.neon.tech/neondb?sslmode=require"
	for _, in := range []string{
		want,
		"  " + want + "\n",
		`"` + want + `"`,
		"psql '" + want + "'",
	} {
		if got := cleanDatabaseURL(in); got != want {
			t.Errorf("cleanDatabaseURL(%q) = %q", in, got)
		}
	}
}
