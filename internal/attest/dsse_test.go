package attest

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestSignVerifyRoundTrip(t *testing.T) {
	priv, pub, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"hello":"world"}`)

	env, err := Sign(priv, "application/vnd.in-toto+json", payload)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if len(env.Signatures) != 1 {
		t.Fatalf("signatures = %d, want 1", len(env.Signatures))
	}
	if env.Signatures[0].KeyID != priv.KeyID {
		t.Errorf("keyid = %q, want %q", env.Signatures[0].KeyID, priv.KeyID)
	}

	got, gotType, err := Verify(pub, env)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("payload mismatch: got %q want %q", got, payload)
	}
	if gotType != "application/vnd.in-toto+json" {
		t.Errorf("payloadType = %q", gotType)
	}
}

func TestVerifyRejectsTamperedPayload(t *testing.T) {
	priv, pub, _ := GenerateKey()
	env, _ := Sign(priv, "application/json", []byte(`{"a":1}`))

	env.Payload = base64.StdEncoding.EncodeToString([]byte(`{"a":2}`))
	if _, _, err := Verify(pub, env); err == nil {
		t.Error("verify accepted tampered payload")
	}
}

func TestVerifyRejectsTamperedType(t *testing.T) {
	priv, pub, _ := GenerateKey()
	env, _ := Sign(priv, "application/json", []byte(`{}`))

	env.PayloadType = "application/x-evil"
	if _, _, err := Verify(pub, env); err == nil {
		t.Error("verify accepted tampered payloadType (PAE should bind it)")
	}
}

func TestVerifyRejectsWrongKey(t *testing.T) {
	priv, _, _ := GenerateKey()
	_, otherPub, _ := GenerateKey()
	env, _ := Sign(priv, "application/json", []byte(`{}`))
	if _, _, err := Verify(otherPub, env); err == nil {
		t.Error("verify accepted signature made by a different key")
	}
}

func TestVerifyRejectsEmptyEnvelope(t *testing.T) {
	_, pub, _ := GenerateKey()
	cases := []*Envelope{
		nil,
		{},
		{PayloadType: "x", Payload: "Zm9v"},
	}
	for i, env := range cases {
		if _, _, err := Verify(pub, env); err == nil {
			t.Errorf("case %d: verify accepted invalid envelope", i)
		}
	}
}

func TestSignRejectsNilOrEmpty(t *testing.T) {
	priv, _, _ := GenerateKey()
	if _, err := Sign(nil, "x", []byte(`{}`)); err == nil {
		t.Error("Sign accepted nil priv")
	}
	if _, err := Sign(priv, "", []byte(`{}`)); err == nil {
		t.Error("Sign accepted empty payloadType")
	}
}

func TestEnvelopeJSONRoundTrip(t *testing.T) {
	priv, pub, _ := GenerateKey()
	env, _ := Sign(priv, "application/vnd.in-toto+json", []byte(`{"x":1}`))

	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"payloadType"`) {
		t.Errorf("envelope JSON missing payloadType: %s", raw)
	}

	var parsed Envelope
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, _, err := Verify(pub, &parsed); err != nil {
		t.Errorf("verify after json round-trip: %v", err)
	}
}

func TestPAEDistinguishesType(t *testing.T) {
	// The whole point of PAE: signing ("a", "bc") must differ from
	// signing ("ab", "c"). Otherwise an attacker could swap type and
	// payload bytes without breaking the signature.
	a := pae("a", []byte("bc"))
	b := pae("ab", []byte("c"))
	if bytes.Equal(a, b) {
		t.Errorf("PAE collision:\n  a = %q\n  b = %q", a, b)
	}
}
