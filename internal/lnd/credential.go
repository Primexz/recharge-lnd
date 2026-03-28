package lnd

import "context"

type MacaroonCredential struct {
	MacaroonHex string
}

func (m *MacaroonCredential) GetRequestMetadata(_ context.Context, _ ...string) (map[string]string, error) {
	return map[string]string{
		"macaroon": m.MacaroonHex,
	}, nil
}

func (m *MacaroonCredential) RequireTransportSecurity() bool {
	return true
}
