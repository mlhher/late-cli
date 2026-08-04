package tui

import (
	"encoding/json"
	"sort"
)

var LateTheme = []byte(`
{
  "document": {
    "block_prefix": "",
    "block_suffix": "",
    "color": "#F3F4F6",
    "background_color": "#0E0E10",
    "margin": 0
  },
  "paragraph": {
    "margin": 0,
    "background_color": "#0E0E10"
  },
  "block_quote": {
    "indent": 1,
    "indent_token": "│ ",
    "color": "#8A94A6",
    "background_color": "#0E0E10"
  },
  "list": {
    "level_indent": 2,
    "background_color": "#0E0E10"
  },
  "bullet": {
    "color": "#E5A85C"
  },
  "enumeration": {
    "color": "#E5A85C",
    "block_suffix": ". "
  },
  "task": {
    "ticked": "[x] ",
    "unticked": "[ ] ",
    "color": "#E5A85C"
  },
  "heading": {
    "block_suffix": "\n",
    "color": "#E5A85C",
    "bold": true
  },
  "h1": {
    "prefix": "# "
  },
  "h2": {
    "prefix": "## "
  },
  "h3": {
    "prefix": "### "
  },
  "strong": {
    "bold": true,
    "color": "#E5A85C"
  },
  "emph": {
    "italic": true,
    "color": "#62B3D5"
  },
  "code": {
    "prefix": " ",
    "suffix": " ",
    "color": "#62B3D5",
    "background_color": "#1B1B1E"
  },
  "code_block": {
    "margin": 0,
    "chroma": {
      "background": {
        "background_color": "#141416"
      },
      "text": {
        "color": "#F3F4F6",
        "background_color": "#141416"
      },
      "error": {
        "color": "#EF4444",
        "background_color": "#141416"
      },
      "comment": {
        "color": "#64748B"
      },
      "keyword": {
        "color": "#E5A85C"
      },
      "literal": {
        "color": "#62B3D5"
      },
      "name_tag": {
        "color": "#62B3D5"
      },
      "operator": {
        "color": "#F3F4F6"
      },
      "string": {
        "color": "#A7C080"
      }
    },
    "background_color": "#141416"
  },
  "table": {
    "center": false,
    "margin": 0,
    "color": "#F3F4F6",
    "background_color": "#0E0E10"
  },
  "table_header": {
    "color": "#E5A85C",
    "background_color": "#0E0E10",
    "bold": true
  },
  "table_cell": {
    "color": "#F3F4F6",
    "background_color": "#0E0E10"
  },
  "link": {
    "color": "#62B3D5",
    "underline": true
  },
  "image": {
    "color": "#62B3D5",
    "underline": true
  }
}
`)

// ResolveRenderTheme merges plugin-provided glamour modifications on top of
// the bundled base theme. The merge is a recursive top-level merge: keys in
// `mod` win, but unmodified keys retain their base values.
//
// `palette` (if non-empty) is surfaced separately so the TUI can apply
// semantic overrides via lipgloss without rebuilding the glamour config.
// This keeps palettes orthogonal to markdown rendering.
func ResolveRenderTheme(name string, mod map[string]any, palette map[string]string) ([]byte, error) {
	if len(mod) == 0 && len(palette) == 0 {
		return LateTheme, nil
	}

	var base map[string]any
	if err := json.Unmarshal(LateTheme, &base); err != nil {
		return nil, err
	}

	for k, v := range mod {
		base[k] = mergeAny(base[k], v)
	}

	// Optional palette overlay is exposed under a special key so consumers can
	// introspect it if they want, but doesn't break glamour's strict schema.
	if len(palette) > 0 {
		keys := make([]string, 0, len(palette))
		for k := range palette {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		p := make(map[string]any, len(palette))
		for _, k := range keys {
			p[k] = palette[k]
		}
		base["_late_palette"] = p
	}

	// Theme name is read by humans debugging glamour; harmless otherwise.
	if name != "" {
		base["_late_theme_name"] = name
	}

	return json.Marshal(base)
}

// mergeAny performs a shallow merge when both sides are maps, otherwise
// the override wins. Returns `override` if `base` is not a map.
func mergeAny(base, override any) any {
	if bm, ok := base.(map[string]any); ok {
		if om, ok := override.(map[string]any); ok {
			for k, v := range om {
				bm[k] = mergeAny(bm[k], v)
			}
			return bm
		}
	}
	return override
}
