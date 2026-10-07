package archtest

import (
	"go/constant"
	"go/token"
	"go/types"
	"testing"
)

// Regression cases for the compatibility-kind classifier. They pin the false
// positive found on integration/irsry-wave3 (the "legacy_kind" tag key) and
// the sanctioned CPStateFamily discriminator exemption.
func TestLegacyKindConstClassification(t *testing.T) {
	kindsPkg := types.NewPackage(modulePath+"/internal/kinds", "kinds")
	adapterPkg := types.NewPackage(modulePath+"/internal/adapters/nostr", "nostr")
	libraryPkg := types.NewPackage("fiatjaf.com/nostr", "nostr")
	familyType := types.NewNamed(types.NewTypeName(token.NoPos, kindsPkg, "CPStateFamily", nil), types.Typ[types.Int], nil)

	intConst := func(pkg *types.Package, name string, value int64) *types.Const {
		return types.NewConst(token.NoPos, pkg, name, types.Typ[types.UntypedInt], constant.MakeInt64(value))
	}
	cases := []struct {
		name  string
		obj   *types.Const
		want  bool
		cause string
	}{
		{"tag key string constant", types.NewConst(token.NoPos, kindsPkg, "CASControlStateTagLegacyKind", types.Typ[types.UntypedString], constant.MakeString("legacy_kind")), false,
			"a string tag key named *LegacyKind is not a kind"},
		{"legacy-named worker kind", intConst(kindsPkg, "LegacyWorkerState", 31974), true, "Legacy* kinds share values with live kinds"},
		{"live kind sharing a legacy value", intConst(kindsPkg, "SystemDiscovery", 31974), false, "31974 is only legacy by name"},
		{"legacy-named interop kind", intConst(kindsPkg, "SoulFactoryActionLegacyResult", 1951), true, "Legacy as a CamelCase word"},
		{"non-word Legacy substring", intConst(kindsPkg, "NonLegacyish", 1), false, "Legacyish is not the word Legacy"},
		{"DNS legacy read-model kind", intConst(kindsPkg, "DNSZoneState", 31975), true, "31975-31978 are legacy wire kinds"},
		{"re-exported alias of a legacy kind", intConst(adapterPkg, "KindDNSZoneState", 31975), true, "aliases are caught by value"},
		{"re-exported Legacy alias", intConst(adapterPkg, "KindLegacyWorkerState", 31974), true, "aliases are caught by name"},
		{"deprecated status kind", intConst(kindsPkg, "DeploymentStatus", 6961), true, "C-44 status families"},
		{"cp-state family discriminator", types.NewConst(token.NoPos, kindsPkg, "CPStateFamilyDNSZone", familyType, constant.MakeInt64(31975)), false,
			"the sanctioned legacy_kind discriminator contract"},
		{"canonical kind", intConst(kindsPkg, "CASControlState", 30900), false, "canonical"},
		{"library constant in a legacy range", intConst(libraryPkg, "Something", 31000), false, "only this module's constants"},
	}
	for _, tc := range cases {
		if got := isLegacyKindConst(tc.obj); got != tc.want {
			t.Errorf("%s: isLegacyKindConst(%s) = %v, want %v (%s)", tc.name, tc.obj.Name(), got, tc.want, tc.cause)
		}
	}
}
