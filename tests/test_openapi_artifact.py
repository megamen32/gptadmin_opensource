from __future__ import annotations

import os
import re
import socket
import subprocess
import tempfile
import time
from difflib import unified_diff
from pathlib import Path
from urllib.error import URLError
from urllib.request import ProxyHandler, build_opener

import yaml


ROOT = Path(__file__).resolve().parent.parent
GO_HUB_ROOT = ROOT / "go-hub"
PUBLIC_OPENAPI = ROOT / "public" / "openapi.yaml"
OPENAPI_ORIGIN_RE = re.compile(r"^  - url: (\S+)$", re.MULTILINE)
DIRECT_OPENER = build_opener(ProxyHandler({}))


def _free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def _public_origin() -> str:
    match = OPENAPI_ORIGIN_RE.search(PUBLIC_OPENAPI.read_text(encoding="utf-8"))
    assert match, "public/openapi.yaml does not declare a servers url"
    return match.group(1)


def _wait_for_openapi(url: str, timeout_seconds: float = 45.0) -> bytes:
    deadline = time.monotonic() + timeout_seconds
    last_error: Exception | None = None
    while time.monotonic() < deadline:
        try:
            with DIRECT_OPENER.open(url, timeout=1.0) as response:
                if response.status == 200:
                    return response.read()
                last_error = RuntimeError(f"unexpected HTTP {response.status}")
        except (OSError, URLError) as exc:
            last_error = exc
        time.sleep(0.2)
    raise AssertionError(f"timed out waiting for {url}: {last_error}")


def _terminate(process: subprocess.Popen[str]) -> None:
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=10)


def _schema(type_: str, **kwargs: object) -> dict[str, object]:
    schema: dict[str, object] = {"type": type_}
    if "enum" in kwargs and kwargs["enum"] is not None:
        kwargs["enum"] = sorted(kwargs["enum"])  # type: ignore[assignment]
    if "required" in kwargs and kwargs["required"] is not None:
        kwargs["required"] = sorted(kwargs["required"])  # type: ignore[assignment]
    schema.update(kwargs)
    return schema


def _schema_ref(ref: str) -> dict[str, str]:
    return {"$ref": ref}


def _json_content(schema: dict[str, object] | None = None) -> dict[str, dict[str, object]]:
    payload: dict[str, object] = {}
    if schema is not None:
        payload["schema"] = schema
    return {"application/json": payload}


def _param(location: str, name: str, required: bool, schema: dict[str, object]) -> dict[str, object]:
    return {"in": location, "name": name, "required": required, "schema": schema}


def _request_body(content: dict[str, dict[str, object]]) -> dict[str, object]:
    return {"required": True, "content": content}


def _response(content: dict[str, dict[str, object]] | None = None) -> dict[str, object]:
    return {} if content is None else {"content": content}


def _security(*requirements: dict[str, list[str]]) -> list[list[list[object]]]:
    return [
        [[scheme, list(scopes)] for scheme, scopes in sorted(requirement.items())]
        for requirement in requirements
    ]


def _schema_signature(schema: dict[str, object] | None) -> dict[str, object] | None:
    if schema is None:
        return None

    signature: dict[str, object] = {}
    for key in ("$ref", "type", "format", "nullable", "additionalProperties", "minimum", "maximum", "minLength", "maxLength", "pattern", "default"):
        if key in schema:
            signature[key] = schema[key]
    if "enum" in schema:
        signature["enum"] = sorted(schema["enum"])  # type: ignore[arg-type]
    if "required" in schema:
        signature["required"] = sorted(schema["required"])  # type: ignore[arg-type]
    if "properties" in schema:
        signature["properties"] = {
            name: _schema_signature(value) for name, value in sorted(schema["properties"].items())  # type: ignore[union-attr]
        }
    if "items" in schema:
        signature["items"] = _schema_signature(schema["items"])  # type: ignore[arg-type]
    if isinstance(schema.get("additionalProperties"), dict):
        signature["additionalProperties"] = _schema_signature(schema["additionalProperties"])  # type: ignore[arg-type]
    return signature


def _content_signature(content: dict[str, dict[str, object]]) -> dict[str, dict[str, object]]:
    return {
        media_type: {
            key: _schema_signature(value) if key == "schema" else value
            for key, value in sorted(media.items())
        }
        for media_type, media in sorted(content.items())
    }


def _parameter_signature(parameter: dict[str, object]) -> dict[str, object]:
    signature = {"in": parameter["in"], "name": parameter["name"], "required": parameter.get("required", False)}
    if "schema" in parameter:
        signature["schema"] = _schema_signature(parameter["schema"])  # type: ignore[arg-type]
    return signature


def _request_body_signature(body: dict[str, object] | None) -> dict[str, object] | None:
    if body is None:
        return None
    signature: dict[str, object] = {"required": body.get("required", False)}
    if "content" in body:
        signature["content"] = _content_signature(body["content"])  # type: ignore[arg-type]
    return signature


def _response_signature(response: dict[str, object]) -> dict[str, object]:
    signature: dict[str, object] = {}
    if "content" in response:
        signature["content"] = _content_signature(response["content"])  # type: ignore[arg-type]
    return signature


def _effective_security(spec: dict[str, object], operation: dict[str, object]) -> list[list[list[object]]]:
    if "security" in operation:
        return _security(*operation["security"])  # type: ignore[arg-type]
    return _security(*spec.get("security", []))  # type: ignore[arg-type]


def _core_operation_contract(spec: dict[str, object]) -> dict[str, dict[str, object]]:
    contract: dict[str, dict[str, object]] = {}
    for path, methods in spec["paths"].items():  # type: ignore[union-attr]
        for method, operation in methods.items():  # type: ignore[union-attr]
            contract[f"{method.upper()} {path}"] = {
                "security": _effective_security(spec, operation),  # type: ignore[arg-type]
                "parameters": sorted(
                    (_parameter_signature(parameter) for parameter in operation.get("parameters", [])),  # type: ignore[arg-type]
                    key=lambda item: (item["in"], item["name"]),
                ),
                "requestBody": _request_body_signature(operation.get("requestBody")),  # type: ignore[arg-type]
                "responses": {
                    status: _response_signature(response)
                    for status, response in sorted(operation.get("responses", {}).items())  # type: ignore[arg-type]
                },
            }
    return contract


EXPECTED_GENERATED_CORE_CONTRACT = {
    "GET /mcp-relay/servers": {
        "security": _security({"bearerAuth": []}),
        "parameters": [_param("query", "detail", False, _schema("string", enum=["full"]))],
        "requestBody": None,
        "responses": {
            "200": _response(_json_content(_schema_ref("#/components/schemas/DiscoverResponse"))),
        },
    },
    "POST /mcp-relay/tools": {
        "security": _security({"bearerAuth": []}),
        "parameters": [],
        "requestBody": _request_body(_json_content(_schema_ref("#/components/schemas/SchemaRequest"))),
        "responses": {
            "200": _response(_json_content(_schema_ref("#/components/schemas/Result"))),
        },
    },
    "POST /mcp-relay/call": {
        "security": _security({"bearerAuth": []}),
        "parameters": [],
        "requestBody": _request_body(_json_content(_schema_ref("#/components/schemas/ExecuteRequest"))),
        "responses": {
            "200": _response(_json_content(_schema_ref("#/components/schemas/Result"))),
            "428": _response(_json_content(_schema_ref("#/components/schemas/ApprovalRequired"))),
            "429": _response(),
        },
    },
    "GET /mcp-relay/job/{job_id}": {
        "security": _security({"bearerAuth": []}),
        "parameters": [
            _param("path", "job_id", True, _schema("string")),
            _param("query", "ack", False, _schema("boolean", default=False)),
        ],
        "requestBody": None,
        "responses": {
            "200": _response(_json_content(_schema_ref("#/components/schemas/Job"))),
        },
    },
    "POST /webhooks/v1/{route}": {
        "security": _security({"webhookToken": []}),
        "parameters": [_param("path", "route", True, _schema("string"))],
        "requestBody": _request_body({"application/json": {}}),
        "responses": {
            "202": _response(),
        },
    },
    "GET /webhook-jobs/{job_id}": {
        "security": _security({"webhookToken": []}),
        "parameters": [_param("path", "job_id", True, _schema("string"))],
        "requestBody": None,
        "responses": {
            "200": _response(),
        },
    },
    "GET /admin/api/webhook-jobs/{job_id}": {
        "security": _security({"bearerAuth": []}),
        "parameters": [_param("path", "job_id", True, _schema("string"))],
        "requestBody": None,
        "responses": {
            "200": _response(),
        },
    },
    "GET /webhook-routes": {
        "security": _security({"bearerAuth": []}),
        "parameters": [],
        "requestBody": None,
        "responses": {
            "200": _response(),
        },
    },
    "POST /webhook-routes": {
        "security": _security({"bearerAuth": []}),
        "parameters": [_param("header", "X-GPTAdmin-Approval-ID", False, _schema("string"))],
        "requestBody": _request_body(_json_content(_schema_ref("#/components/schemas/WebhookRoute"))),
        "responses": {
            "201": _response(),
        },
    },
    "PUT /webhook-routes/{route}": {
        "security": _security({"bearerAuth": []}),
        "parameters": [
            _param("header", "X-GPTAdmin-Approval-ID", False, _schema("string")),
            _param("path", "route", True, _schema("string")),
        ],
        "requestBody": _request_body(_json_content(_schema_ref("#/components/schemas/WebhookRoute"))),
        "responses": {
            "200": _response(),
        },
    },
    "DELETE /webhook-routes/{route}": {
        "security": _security({"bearerAuth": []}),
        "parameters": [
            _param("header", "X-GPTAdmin-Approval-ID", False, _schema("string")),
            _param("path", "route", True, _schema("string")),
        ],
        "requestBody": None,
        "responses": {
            "204": _response(),
        },
    },
    "POST /proxy-control/v1/request": {
        "security": _security({"bearerAuth": []}),
        "parameters": [],
        "requestBody": _request_body(
            _json_content(
                _schema(
                    "object",
                    required=["profile_id", "policy"],
                    properties={
                        "policy": _schema("object", additionalProperties=True),
                        "profile_id": _schema("string"),
                    },
                )
            )
        ),
        "responses": {
            "201": _response(),
        },
    },
    "POST /proxy-control/v1/approve": {
        "security": _security({"bearerAuth": []}),
        "parameters": [],
        "requestBody": _request_body(
            _json_content(
                _schema(
                    "object",
                    required=["capability_id"],
                    properties={"capability_id": _schema("string")},
                )
            )
        ),
        "responses": {
            "200": _response(),
        },
    },
    "POST /proxy-control/v1/issue": {
        "security": _security({"bearerAuth": []}),
        "parameters": [],
        "requestBody": _request_body(
            _json_content(
                _schema(
                    "object",
                    required=["capability_id", "target"],
                    properties={
                        "capability_id": _schema("string"),
                        "target": _schema("string"),
                    },
                )
            )
        ),
        "responses": {
            "200": _response(),
        },
    },
    "POST /proxy-control/v1/open": {
        "security": _security({"bearerAuth": []}),
        "parameters": [],
        "requestBody": _request_body(
            _json_content(
                _schema(
                    "object",
                    required=["token", "role"],
                    properties={
                        "token": _schema("string"),
                        "role": _schema("string", enum=["client", "agent"]),
                    },
                )
            )
        ),
        "responses": {
            "200": _response(),
        },
    },
    "GET /proxy-control/v1/status": {
        "security": _security({"bearerAuth": []}),
        "parameters": [_param("query", "capability_id", True, _schema("string"))],
        "requestBody": None,
        "responses": {
            "200": _response(),
        },
    },
    "POST /proxy-control/v1/revoke": {
        "security": _security({"bearerAuth": []}),
        "parameters": [],
        "requestBody": _request_body(
            _json_content(
                _schema(
                    "object",
                    required=["capability_id"],
                    properties={"capability_id": _schema("string")},
                )
            )
        ),
        "responses": {
            "200": _response(),
        },
    },
}


def _render_diff(label: str, expected: dict[str, object], actual: dict[str, object], log_path: Path) -> str:
    diff = "\n".join(
        unified_diff(
            yaml.safe_dump(expected, sort_keys=True).splitlines(),
            yaml.safe_dump(actual, sort_keys=True).splitlines(),
            fromfile=f"expected {label}",
            tofile=f"actual {label}",
            lineterm="",
        )
    )
    tail = log_path.read_text(encoding="utf-8")
    return f"OpenAPI artifact mismatch\n\n{diff}\n\nRenderer log:\n{tail}"


def test_public_openapi_artifact_matches_go_hub_renderer():
    """The public OpenAPI artifact must preserve the hub-generated core contract."""

    expected = yaml.safe_load(PUBLIC_OPENAPI.read_text(encoding="utf-8"))
    origin = _public_origin()
    port = _free_port()

    with tempfile.TemporaryDirectory(prefix="gptadmin-openapi-") as tmpdir:
        env = os.environ.copy()
        env.update(
            {
                "GPTADMIN_ROOT": str(ROOT),
                "GPTADMIN_CONFIG_DIR": str(Path(tmpdir) / "config"),
                "GPTADMIN_ARTIFACT_DIR": str(Path(tmpdir) / "build"),
                "PUBLIC_ORIGIN": origin,
                "HUB_HOST": "127.0.0.1",
                "HUB_PORT": str(port),
            }
        )

        log_path = Path(tmpdir) / "gohub.log"
        with log_path.open("w", encoding="utf-8") as log_file:
            process = subprocess.Popen(
                ["go", "run", "./cmd/gptadmin-hub"],
                cwd=GO_HUB_ROOT,
                env=env,
                stdout=log_file,
                stderr=subprocess.STDOUT,
                text=True,
            )

            actual = b""
            try:
                actual = _wait_for_openapi(f"http://127.0.0.1:{port}/actions/openapi.yaml")
            finally:
                _terminate(process)

        generated = yaml.safe_load(actual.decode("utf-8"))
        expected_paths = set(expected["paths"]) - {"/connect.json"}
        generated_paths = set(generated["paths"])
        assert expected_paths >= generated_paths, _render_diff("paths", expected["paths"], generated["paths"], log_path)

        expected_schemas = set(expected["components"]["schemas"])
        generated_schemas = set(generated["components"]["schemas"])
        assert expected_schemas >= generated_schemas, _render_diff("schemas", expected["components"]["schemas"], generated["components"]["schemas"], log_path)

        assert "/connect.json" in expected["paths"], "public/openapi.yaml lost the connection manifest extension"
        for schema_name in ("ConnectionManifest", "ConnectionClient"):
            assert schema_name in expected["components"]["schemas"], f"public/openapi.yaml lost {schema_name}"
        for schema_name in ("NetworkProxyPolicy", "ProxyStreamGrant"):
            assert schema_name in expected["components"]["schemas"], f"public/openapi.yaml lost {schema_name}"


def test_generated_core_openapi_operations_have_structural_contract():
    """The generated core OpenAPI surface should stay structurally stable."""

    origin = _public_origin()
    port = _free_port()

    with tempfile.TemporaryDirectory(prefix="gptadmin-openapi-") as tmpdir:
        env = os.environ.copy()
        env.update(
            {
                "GPTADMIN_ROOT": str(ROOT),
                "GPTADMIN_CONFIG_DIR": str(Path(tmpdir) / "config"),
                "GPTADMIN_ARTIFACT_DIR": str(Path(tmpdir) / "build"),
                "PUBLIC_ORIGIN": origin,
                "HUB_HOST": "127.0.0.1",
                "HUB_PORT": str(port),
            }
        )

        log_path = Path(tmpdir) / "gohub.log"
        with log_path.open("w", encoding="utf-8") as log_file:
            process = subprocess.Popen(
                ["go", "run", "./cmd/gptadmin-hub"],
                cwd=GO_HUB_ROOT,
                env=env,
                stdout=log_file,
                stderr=subprocess.STDOUT,
                text=True,
            )

            actual = b""
            try:
                actual = _wait_for_openapi(f"http://127.0.0.1:{port}/actions/openapi.yaml")
            finally:
                _terminate(process)

        generated = yaml.safe_load(actual.decode("utf-8"))
        actual_contract = _core_operation_contract(generated)
        assert set(actual_contract) == {
            "GET /mcp-relay/servers",
            "POST /mcp-relay/tools",
            "POST /mcp-relay/call",
            "GET /mcp-relay/job/{job_id}",
        }, _render_diff("generated core contract", EXPECTED_GENERATED_CORE_CONTRACT, actual_contract, log_path)
        assert all(
            operation["security"] == _security({"bearerAuth": []})
            for operation in actual_contract.values()
        )
        assert actual_contract["GET /mcp-relay/job/{job_id}"]["parameters"] == [
            _param("path", "job_id", True, _schema("string")),
        ]
        assert actual_contract["POST /mcp-relay/tools"]["requestBody"]["required"] is True  # type: ignore[index]
        assert actual_contract["POST /mcp-relay/call"]["requestBody"]["required"] is True  # type: ignore[index]
