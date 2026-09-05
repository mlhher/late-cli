package tui

var LateTheme = []byte(`
{
  "document": {
    "block_prefix": "",
    "block_suffix": "",
    "color": "#F3F4F6",
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
    "color": "#8A94A6",
    "background_color": "#0B0C0E"
  },
  "list": {
    "level_indent": 2,
    "background_color": "#0B0C0E"
  },
  "bullet": {
    "color": "#F5A742"
  },
  "enumeration": {
    "color": "#F5A742",
    "block_suffix": ". "
  },
  "task": {
    "ticked": "[x] ",
    "unticked": "[ ] ",
    "color": "#4ECCA3"
  },
  "heading": {
    "block_suffix": "\n",
    "color": "#F5A742",
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
    "color": "#F5A742"
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
        "color": "#F3F4F6"
      },
      "error": {
        "color": "#FF6B6B"
      },
      "comment": {
        "color": "#5C6370"
      },
      "keyword": {
        "color": "#F5A742"
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
    "color": "#F3F4F6",
    "background_color": "#0B0C0E"
  },
  "table_header": {
    "color": "#F5A742",
    "background_color": "#0B0C0E",
    "bold": true
  },
  "table_cell": {
    "color": "#F3F4F6",
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
