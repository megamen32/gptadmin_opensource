# Typed MCP calls and scripts without nested shell quoting

## Prefer MCP contracts over shell

Use the OpenCode-style sequence: find tools with `search({query,limit})`,
inspect `mcp.schema(target,tool)` when needed, and invoke the chosen
`tools["<exact target>"].tool({named: "arguments"})`.

The Hub validates the tool's complete JSON Schema **before dispatch**, including
types, required properties, enums, array items, ranges and `oneOf`. If a schema
cannot be fetched for a nonbuilt-in tool, `tools[]` returns an explicit failure
without executing it. Failed input validation reports a JSON-pointer field,
schema keyword, expected type and actual type; it does not echo argument
values. The caller's access profile, approvals and tool restrictions continue
to apply.

Direct `mcp.call` is retained for compatibility: built-in tool contracts
are also checked by the Hub, and explicitly inspected external schemas are
validated within that Code Mode run. Prefer `tools[]` for any external MCP
rather than inventing a command-line equivalent.


**Preferred order**

1. Use structured MCP tools directly. For writing/editing files, use `file_editor.create` or `mcp.files.create` instead of a shell heredoc, `python -c`, base64 upload or `echo`.
2. For an executable and its arguments, use `mcp.shell(target, [program, ...args])`. This is native argv, not a Bash string.
3. For multiline Python, Ruby or Node code, pass `stdin` separately. The child receives the exact UTF-8 source, without shell interpolation.
4. Use legacy `cmd` only for intentional shell features such as pipelines and redirection. Never embed another language into nested shell-quoted arguments.

## Code Mode examples

### Literal command arguments

```javascript
const r = await mcp.shell('shell:node-100', ['printf', '%s', `it's "quoted"; $HOME`]);
return r.stdout;
```

### Python source via stdin

```javascript
const source = String.raw`
import json
print(json.dumps({'name': "it's quoted", 'message': 'Привет 🌍'}, ensure_ascii=False))
`;
const r = await mcp.shell('shell:node-100', ['python3', '-'], {stdin: source});
return {exit_code: r.exit_code, stdout: r.stdout, stderr: r.stderr};
```

### Ruby/Rails in an existing Docker container

```javascript
const ruby = String.raw`
puts({message: "it's working", total: 1 + 2}.inspect)
`;
const r = await mcp.shell('shell:node-44',
  ['docker', 'exec', '-i', 'web-container', 'bundle', 'exec', 'rails', 'runner', '-'],
  {stdin: ruby, timeout: 30});
return r;
```

Use `-i` for Docker stdin. Confirm the container and Rails command support source from stdin. An executable's stdin is supplied as text; it is not automatically a script interpreter.

### Persistent scripts or source containing backticks

Use `mcp.files.create('file:node-100', '/tmp/inspect.rb', source)` (or the `file_editor` MCP tool with `action: 'create'`) to write the exact source. Then use native argv to run the saved file. Prefer this approach when the source contains JavaScript template delimiters or exceeds the Code Mode source budget.

## Limits and reliability

- `argv` is 1–256 strings, total at most 64 KiB. It never goes through `sh -c` when the executor supports native argv.
- `stdin` is UTF-8 text at most 256 KiB; invalid UTF-8 and NUL are refused before queueing. It requires ShellMCP build **1074 or newer** on the target. Older nodes fail explicitly rather than silently discarding script input. Longer data belongs in a file or resource.
- `argv` and `stdin` use the same authentication, access profiles, approvals, task queue and deadlines as `cmd`. Do not retry an unconfirmed running task; inspect its task/job id.
- The ShellMCP SSH and Android Shizuku backends do not emulate native argv through shell quoting. Unsupported combinations fail explicitly.
- Task previews omit stdin contents to avoid accidentally showing script bodies. Task records still retain the authorized arguments for execution. Never embed credentials in source code; use configured secret references and environment facilities.
- If a command is expected to run in the background, use native MCP Tasks. Do not use broad `pkill -f` on a command string that can match its own invocation.

Regression tests: `go-shellmcp/internal/shell/stdin_test.go`, `go-shellmcp/internal/server/stdin_mcp_test.go`, `go-hub/internal/hub/code_mode_stdin_test.go`.
