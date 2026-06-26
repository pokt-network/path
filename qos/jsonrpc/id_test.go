package jsonrpc

import "testing"

func TestIDString(t *testing.T) {
	tests := []struct {
		name string
		id   ID
		want string
	}{
		{"int", IDFromInt(123456), "123456"},
		{"int zero", IDFromInt(0), "0"},
		{"int negative", IDFromInt(-7), "-7"},
		{"string", IDFromStr("abc"), "abc"},
		{"empty/null", ID{}, "null"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.id.String(); got != tt.want {
				t.Fatalf("ID.String() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestIDEqual_CrossType guards the cross-type comparison path that relies on
// String() (int id vs the same value as a string).
func TestIDEqual_CrossType(t *testing.T) {
	if !IDFromInt(42).Equal(IDFromStr("42")) {
		t.Fatal("IDFromInt(42) should equal IDFromStr(\"42\")")
	}
	if IDFromInt(42).Equal(IDFromStr("43")) {
		t.Fatal("IDFromInt(42) should not equal IDFromStr(\"43\")")
	}
}
