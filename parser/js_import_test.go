package parser

import "testing"

func TestJSImportsAndReExports(t *testing.T) {
	for _, language := range []string{"javascript", "typescript", "tsx"} {
		t.Run(language, func(t *testing.T) {
			type testCase struct {
				name string
				src  string
				want string
			}
			cases := []testCase{
				{"named import", `import { value as renamed } from './named';`, "./named"},
				{"default import", `import value from "./default";`, "./default"},
				{"namespace import", `import * as values from './namespace';`, "./namespace"},
				{"side effect import", `import './setup';`, "./setup"},
				{"named re-export", `export { value, other as renamed } from './named';`, "./named"},
				{"star re-export", `export * from "./all";`, "./all"},
				{"namespace re-export", `export * as values from './namespace';`, "./namespace"},
				{"local export", `const value = 1; export { value };`, ""},
				{"export declaration", `export const value = './not-an-import';`, ""},
				{"dynamic import", `const promise = import('./dynamic');`, ""},
			}
			if language != "javascript" {
				cases = append(cases, testCase{
					"type re-export", `export type { Value } from './types';`, "./types",
				})
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					result, err := ParseBytes([]byte(tc.src), "barrel", language)
					if err != nil {
						t.Fatal(err)
					}
					if tc.want == "" {
						if len(result.Imports) != 0 {
							t.Fatalf("imports = %+v, want none", result.Imports)
						}
						return
					}
					if len(result.Imports) != 1 || result.Imports[0].RawPath != tc.want || result.Imports[0].Language != language {
						t.Fatalf("imports = %+v, want one %s import of %q", result.Imports, language, tc.want)
					}
				})
			}
		})
	}
}
