package tui

import (
	"encoding/json"
)

var DefaultThemeEntry = ThemeEntry{
	ID:         "default",
	PluginName: "built-in",
	ThemeName:  "default",
	Glamour:    nil,
}

var LateTheme = []byte(`
{
  "document": {
    "block_prefix": "",
    "block_suffix": "",
    "color": "#E6EDF3",
    "background_color": "#0B0C0E",
    "margin": 0
  },
  "paragraph": {
    "margin": 0,
    "background_color": "#0B0C0E"
  },
  "block_quote": {
    "indent": 1,
    "indent_token": "▎ ",
    "color": "#7D8590",
    "background_color": "#0B0C0E"
  },
  "list": {
    "level_indent": 2,
    "background_color": "#0B0C0E"
  },
  "item": {
    "block_prefix": "• ",
    "color": "#E5A85C"
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
    "color": "#4ECCA3"
  },
  "heading": {
    "block_suffix": "\n",
    "color": "#E5A85C",
    "bold": true
  },
  "h1": {
    "prefix": "▎ "
  },
  "h2": {
    "prefix": "◆ "
  },
  "h3": {
    "prefix": "▸ "
  },
  "strong": {
    "bold": true,
    "color": "#E5A85C"
  },
  "emph": {
    "italic": true,
    "color": "#56B6C2"
  },
  "code": {
    "prefix": " ",
    "suffix": " ",
    "color": "#56B6C2",
    "background_color": "#171922"
  },
  "code_block": {
    "margin": 0,
    "chroma": {
      "text": {
        "color": "#E6EDF3"
      },
      "error": {
        "color": "#E06C75"
      },
      "comment": {
        "color": "#7D8590"
      },
      "keyword": {
        "color": "#E5A85C"
      },
      "literal": {
        "color": "#56B6C2"
      },
      "name_tag": {
        "color": "#61AFEF"
      },
      "operator": {
        "color": "#E5E9F0"
      },
      "string": {
        "color": "#98C379"
      }
    }
  },
  "table": {
    "center": false,
    "margin": 0,
    "color": "#E6EDF3",
    "background_color": "#0B0C0E"
  },
  "table_header": {
    "color": "#E5A85C",
    "background_color": "#0B0C0E",
    "bold": true
  },
  "table_cell": {
    "color": "#E6EDF3",
    "background_color": "#0B0C0E"
  },
  "link": {
    "color": "#56B6C2",
    "underline": true
  },
  "image": {
    "color": "#56B6C2",
    "underline": true
  }
}
`)

// ResolveRenderTheme merges plugin-provided glamour modifications on top of
// the bundled base theme. The merge is a recursive top-level merge: keys in
// `mod` win, but unmodified keys retain their base values.
func ResolveRenderTheme(name string, mod map[string]any) ([]byte, error) {
	if len(mod) == 0 {
		return LateTheme, nil
	}

	var base map[string]any
	if err := json.Unmarshal(LateTheme, &base); err != nil {
		return nil, err
	}

	for k, v := range mod {
		base[k] = mergeAny(base[k], v)
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
