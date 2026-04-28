# ps_bridge.ps1 — AST-backed command parser for the late agent.
# Runs as a persistent process. Each iteration reads one newline-delimited JSON
# request from stdin ({"cmd":"..."}), parses the command with
# System.Management.Automation.Language.Parser::ParseInput, walks the
# resulting AST, and writes one compact JSON IR line to stdout.
# Repeats until stdin is closed (EOF), then exits.
#
# CONTRACT:
#   - This script NEVER executes the input command.
#   - Each response is exactly one line of compact JSON matching ParsedIR schema.
#   - On any error the script still emits valid JSON with risk_flag "syntax_error".
#   - Exit code is always 0 (errors are encoded in the JSON payload).
#   - Each response line is flushed immediately so the Go caller can read it.

param()

Set-StrictMode -Off
$ErrorActionPreference = 'Stop'

function New-IR {
    return @{
        version      = "1"
        platform     = "windows"
        commands     = [System.Collections.Generic.List[string]]::new()
        operators    = [System.Collections.Generic.List[string]]::new()
        redirects    = [System.Collections.Generic.List[string]]::new()
        expansions   = [System.Collections.Generic.List[string]]::new()
        risk_flags   = [System.Collections.Generic.List[string]]::new()
        parse_errors = [System.Collections.Generic.List[string]]::new()
    }
}

function Add-Unique {
    param([System.Collections.Generic.List[string]]$List, [string]$Value)
    if (-not $List.Contains($Value)) { $List.Add($Value) | Out-Null }
}

# --- Risky cmdlet names (lower-case) ---
$riskyCmdlets = @(
    'invoke-expression', 'iex',
    'start-process',     'saps',
    'invoke-command',    'icm',
    'new-object',
    'remove-item',       'ri', 'del', 'erase', 'rd', 'rmdir', 'rm',
    'rename-item',       'rni', 'ren',
    'move-item',         'mi', 'move', 'mv',
    'copy-item',         'ci', 'copy', 'cp',
    'set-content',       'sc',
    'add-content',       'ac',
    'out-file',
    'clear-content',     'clc',
    'set-itemproperty',  'sp',
    'set-acl'
)

# cd / Set-Location aliases — policy engine blocks these.
$cdCmdlets = @(
    'set-location', 'sl', 'cd', 'chdir',
    'push-location', 'pushd',
    'pop-location',  'popd'
)

# Path-creation cmdlets (used for the new-path carveout signal).
$newPathCmdlets = @('mkdir', 'md', 'new-item', 'ni')

# Invoke-Parse parses one command string and returns an IR hashtable.
# Never throws — all errors are encoded inside the returned IR.
function Invoke-Parse {
    param([string]$command)

    $ir = New-IR

    # --- Parse ---
    $tokens = $null
    try {
        $tokenArr = [System.Management.Automation.Language.Token[]]@()
        $errorArr = [System.Management.Automation.Language.ParseError[]]@()
        $ast = [System.Management.Automation.Language.Parser]::ParseInput(
            $command, [ref]$tokenArr, [ref]$errorArr
        )
        $tokens      = $tokenArr
        $parseErrors = $errorArr
    } catch {
        Add-Unique $ir.risk_flags "syntax_error"
        $ir.parse_errors.Add($_.ToString()) | Out-Null
        return $ir
    }

    # Record parser diagnostics (soft errors — PS parser is lenient).
    foreach ($e in $parseErrors) {
        $ir.parse_errors.Add($e.Message) | Out-Null
        Add-Unique $ir.risk_flags "syntax_error"
    }

    # --- Walk every AST node ---
    $allNodes = $ast.FindAll({ $true }, $true)
    foreach ($node in $allNodes) {
        # Commands
        if ($node -is [System.Management.Automation.Language.CommandAst]) {
            $elems = $node.CommandElements
            if ($elems -and $elems.Count -gt 0) {
                $cmdName = $elems[0].ToString().Trim().ToLower()
                if ($cmdName -ne '') {
                    Add-Unique $ir.commands $cmdName
                    if ($riskyCmdlets -contains $cmdName) {
                        Add-Unique $ir.risk_flags "invoke_expression"
                    }
                    if ($cdCmdlets -contains $cmdName) {
                        Add-Unique $ir.risk_flags "cd"
                    }
                    if ($newPathCmdlets -contains $cmdName) {
                        Add-Unique $ir.risk_flags "new_path"
                    }
                }
            }
            continue
        }

        # Pipeline: | operator
        if ($node -is [System.Management.Automation.Language.PipelineAst]) {
            if ($node.PipelineElements.Count -gt 1) {
                Add-Unique $ir.operators "|"
                Add-Unique $ir.risk_flags "operator"
            }
            continue
        }

        # && and || via PipelineChain (PS7+). Guard with -is check so older PS
        # versions skip gracefully (the type simply won't exist).
        if ($node.GetType().Name -eq 'PipelineChainAst') {
            try {
                $op = $node.Operator.ToString()
                Add-Unique $ir.operators $op
                Add-Unique $ir.risk_flags "operator"
            } catch {}
            continue
        }

        # File redirection (> >>)
        if ($node -is [System.Management.Automation.Language.FileRedirectionAst]) {
            Add-Unique $ir.redirects "FileRedirection"
            Add-Unique $ir.risk_flags "redirect"
            continue
        }

        # Merging redirection (2>&1 etc.) — not inherently risky, just record it.
        if ($node -is [System.Management.Automation.Language.MergingRedirectionAst]) {
            Add-Unique $ir.redirects "MergingRedirection"
            continue
        }

        # $(...) sub-expression
        if ($node -is [System.Management.Automation.Language.SubExpressionAst]) {
            Add-Unique $ir.expansions "subshell"
            Add-Unique $ir.risk_flags "subshell"
            continue
        }

        # Variable: $var — only count top-level variable references, not those
        # that are children of SubExpressionAst (already counted as subshell).
        if ($node -is [System.Management.Automation.Language.VariableExpressionAst]) {
            Add-Unique $ir.expansions "var"
            Add-Unique $ir.risk_flags "expansion"
            continue
        }

        # Script-block expression: { ... }
        if ($node -is [System.Management.Automation.Language.ScriptBlockExpressionAst]) {
            Add-Unique $ir.expansions "script_block"
            Add-Unique $ir.risk_flags "subshell"
            continue
        }
    }

    # Detect ';' statement separator from top-level statement count.
    try {
        $endBlock = $ast.EndBlock
        if ($endBlock -and $endBlock.Statements -and $endBlock.Statements.Count -gt 1) {
            Add-Unique $ir.operators ";"
            Add-Unique $ir.risk_flags "operator"
        }
    } catch {}

    # Scan tokens for -EncodedCommand / -enc flags and &&/|| (PS5.1 fallback
    # since PipelineChainAst only exists in PS7+).
    foreach ($tok in $tokens) {
        $tv = $tok.Text.ToLower()
        switch ($tv) {
            '-encodedcommand' { Add-Unique $ir.risk_flags "invoke_expression" }
            '-enc'            { Add-Unique $ir.risk_flags "invoke_expression" }
            '-en'             { Add-Unique $ir.risk_flags "invoke_expression" }
            '&&' {
                Add-Unique $ir.operators "&&"
                Add-Unique $ir.risk_flags "operator"
            }
            '||' {
                Add-Unique $ir.operators "||"
                Add-Unique $ir.risk_flags "operator"
            }
        }
    }

    return $ir
}

# Emit-IR serializes an IR hashtable to a compact JSON line and flushes stdout
# immediately so the Go reader is not left waiting for a buffer to fill.
function Emit-IR {
    param($ir)
    $out = [ordered]@{
        version      = $ir.version
        platform     = $ir.platform
        commands     = @($ir.commands)
        operators    = @($ir.operators)
        redirects    = @($ir.redirects)
        expansions   = @($ir.expansions)
        risk_flags   = @($ir.risk_flags)
        parse_errors = @($ir.parse_errors)
    }
    [Console]::Out.WriteLine(($out | ConvertTo-Json -Compress -Depth 3))
    [Console]::Out.Flush()
}

# --- Main persistent loop ---
# Each iteration: read one JSON request line {"cmd":"..."}, parse, emit one
# JSON response line. Exits when stdin reaches EOF (Go closes the pipe).
while ($true) {
    $line = $null
    try {
        $line = [Console]::In.ReadLine()
    } catch {
        break
    }
    if ($null -eq $line) { break }
    $line = $line.Trim()
    if ($line -eq '') { continue }

    $ir = $null
    try {
        $req = ConvertFrom-Json $line
        $ir = Invoke-Parse $req.cmd
    } catch {
        $ir = New-IR
        Add-Unique $ir.risk_flags "syntax_error"
        $ir.parse_errors.Add("request error: $_") | Out-Null
    }

    Emit-IR $ir
}
