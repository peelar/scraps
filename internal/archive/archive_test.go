package archive

import "testing"

func TestCleanEntryName(t *testing.T) {
	valid := map[string]string{
		"a.txt":         "a.txt",
		"./a.txt":       "a.txt",
		"dir/sub/f.txt": "dir/sub/f.txt",
		"dir/":          "dir",
		"a/./b.txt":     "a/b.txt",
	}
	for input, want := range valid {
		got, err := CleanEntryName(input)
		if err != nil || got != want {
			t.Fatalf("CleanEntryName(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	invalid := []string{"", ".", "/", "..", "../x", "a/../../x", ".scrap", ".scrap/tmp/x"}
	for _, input := range invalid {
		if _, err := CleanEntryName(input); err == nil {
			t.Fatalf("CleanEntryName(%q) = nil error, want rejection", input)
		}
	}
}
