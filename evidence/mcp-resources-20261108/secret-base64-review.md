# M18L9: secret redaction for binary stdout

Bug: M18-SECRET-BASE64.
Base: 3191a8d5e2f4547ef36cbe2102ad28feb117f8af (including incoming file_transfer/stdout_base64 work).

The new stdout_base64 field preserved exact stdout tail bytes before the Hub's ordinary secret redaction. Literal string replacement in the public receipt and persistence boundary did not recognize encoded secret values. The focused regression fails before the production fix for ANSI and invalid-UTF8 stdout at both boundaries.

The shared redactor now recognizes only the stdout_base64 field when SecretValues are present: decode, apply the existing byte-preserving string redaction, then encode again. Malformed or non-string encoded output is omitted because its redaction cannot be verified. Without secret values, the base64 field is unchanged. Other binary stdout bytes, including invalid UTF-8 and control bytes, are preserved; unrelated secret values do not alter them.

The test uses a clearly artificial fixture secret and in-memory shellJob data. It exercises shellJobResponse and persistableShellResult, including malformed encoding and wrong field type. No real credential store was read, no secret value was logged, no production command was executed, and no runtime acceptance is claimed. Existing related secret resolution/relay regression tests also pass.

Only secret_tools.go, the new boundary regression file, one Fixed entry in CHANGELOG.md, and this evidence were changed. The incoming file_transfer and binary stdout generator features remain intact.

Commands, exit codes, timings, and tested file hashes are in secret-base64-checks.json. Before the fix, secret-base64-red.txt has exit 1; after the fix, secret-base64-green.txt has exit 0; secret-base64-related.txt has exit 0.
