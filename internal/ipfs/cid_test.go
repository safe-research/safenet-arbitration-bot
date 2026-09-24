package ipfs

import "testing"

func TestCompute(t *testing.T) {
	for _, tc := range []struct {
		content string
		cid     string
	}{
		{"", "bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku"},
		{"hello world", "bafkreifzjut3te2nhyekklss27nh3k72ysco7y32koao5eei66wof36n5e"},
	} {
		if got := Compute([]byte(tc.content)).String(); got != tc.cid {
			t.Errorf("Compute(%q): got %s, want %s", tc.content, got, tc.cid)
		}
	}
}

func TestParseCID(t *testing.T) {
	const s = "bafkreih7rr54gpwialfg544kfhmjy5axojvhucyv57ll2sw6u6tzyilgpy"
	cid, err := ParseCID(s)
	if err != nil {
		t.Fatalf("ParseCID: %v", err)
	}
	if cid.String() != s {
		t.Errorf("String: got %s, want %s", cid, s)
	}

	if cid, _ := ParseCID("bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku"); cid != Compute(nil) {
		t.Errorf("ParseCID: CID of empty content does not equal Compute(nil)")
	}
}

func TestParseCIDRejectsUnsupported(t *testing.T) {
	for name, s := range map[string]string{
		"empty":              "",
		"CIDv0":              "QmbWqxBEKC3P8tqsKc98xmWNzrzDtRLMiMPL8wBuTGsMnR",
		"ipfs URL":           "ipfs://bafkreih7rr54gpwialfg544kfhmjy5axojvhucyv57ll2sw6u6tzyilgpy",
		"uppercase base32":   "BAFKREIH7RR54GPWIALFG544KFHMJY5AXOJVHUCYV57LL2SW6U6TZYILGPY",
		"dag-pb codec":       "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi",
		"truncated digest":   "bafkreih7rr54gpwialfg544kfhmjy5axojvhucyv57ll2sw6u6tzyil",
		"invalid characters": "bafkreih7rr54gpwialfg544kfhmjy5axojvhucyv57ll2sw6u6tzyilgp1",
	} {
		if _, err := ParseCID(s); err == nil {
			t.Errorf("ParseCID(%s): expected an error for %q", name, s)
		}
	}
}
