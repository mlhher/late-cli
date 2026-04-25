package tui

var LateTheme = []byte(`
{
  "document": {
    "block_prefix": "",
    "block_suffix": "",
    "color": "#ECF0F1",
    "background_color": "#191919",
    "margin": 0
  },
  "paragraph": {
    "margin": 0,
    "background_color": "#191919"
  },
  "block_quote": {
    "indent": 1,
    "indent_token": "│ ",
    "color": "#BDC3C7",
    "background_color": "#191919"
  },
  "list": {
    "level_indent": 2,
    "background_color": "#191919"
  },
  "bullet": {
    "color": "#9B59B6"
  },
  "enumeration": {
    "color": "#9B59B6",
    "block_suffix": ". "
  },
  "task": {
    "ticked": "[x] ",
    "unticked": "[ ] ",
    "color": "#9B59B6"
  },
  "heading": {
    "block_suffix": "\n",
    "color": "#9B59B6",
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
    "color": "#E67E22"
  },
  "emph": {
    "italic": true,
    "color": "#F1C40F"
  },
  "code": {
    "prefix": " ",
    "suffix": " ",
    "color": "#2ECC71",
    "background_color": "#191919"
  },
  "code_block": {
    "margin": 0,
    "chroma": {
      "background": {
        "background_color": "#191919"
      },
      "text": {
        "color": "#ECF0F1",
        "background_color": "#191919"
      },
      "error": {
        "color": "#F1F1F1",
        "background_color": "#191919"
      },
      "comment": {
        "color": "#7F8C8D"
      },
      "keyword": {
        "color": "#9B59B6"
      },
      "literal": {
        "color": "#2ECC71"
      },
      "name_tag": {
        "color": "#2980B9"
      },
      "operator": {
        "color": "#ECF0F1"
      },
      "string": {
        "color": "#F1C40F"
      }
    },
    "background_color": "#191919"
  },
  "table": {
    "center": false,
    "margin": 0,
    "color": "#ECF0F1",
    "background_color": "#191919"
  },
  "table_header": {
    "color": "#9B59B6",
    "background_color": "#191919",
    "bold": true
  },
  "table_cell": {
    "color": "#ECF0F1",
    "background_color": "#191919"
  },
  "link": {
    "color": "#3498DB",
    "underline": true
  },
  "image": {
    "color": "#3498DB",
    "underline": true
  }
}
`)
