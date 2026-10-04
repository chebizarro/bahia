package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/stretchr/testify/require"
)

func TestPackageCLIIntentMatchesCommittedFixtures(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "web", "tests", "fixtures", "package-intents.json"))
	require.NoError(t, err)
	var fixture struct {
		Cases []struct {
			Name    string `json:"name"`
			Request struct {
				Coordinate string          `json:"coordinate"`
				Content    json.RawMessage `json:"content"`
			} `json:"request"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.NotEmpty(t, fixture.Cases)
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			var command any
			switch tc.Name {
			case "promote":
				command = &controlplane.PackagePromotionCommand{}
			case "yank":
				command = &controlplane.PackageYankCommand{}
			default:
				t.Skip("fixture operation has no matching CLI command")
			}
			require.NoError(t, json.Unmarshal(tc.Request.Content, command))
			content, coordinate, err := packageMutationContent(tc.Name, command)
			require.NoError(t, err)
			require.Equal(t, tc.Request.Coordinate, coordinate)
			actual, err := json.Marshal(content)
			require.NoError(t, err)
			require.JSONEq(t, string(tc.Request.Content), string(actual))
		})
	}
}

func TestPackageRepositoryApplyCoordinateIsStableByName(t *testing.T) {
	content, coordinate, err := packageMutationContent("repository-apply", controlplane.PackageRepositoryApplyCommand{Name: "packages"})
	require.NoError(t, err)
	require.Equal(t, "packages", coordinate)
	require.NotContains(t, content, "id")
	id := uuid.New()
	content, coordinate, err = packageMutationContent("repository-apply", controlplane.PackageRepositoryApplyCommand{RepositoryID: id, Name: "packages"})
	require.NoError(t, err)
	require.Equal(t, id.String(), coordinate)
	require.Equal(t, id.String(), content["id"])
}
