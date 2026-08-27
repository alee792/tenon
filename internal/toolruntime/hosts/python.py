"""Tenon's Python tool host.

One long-lived process serves every ``tools/*.py`` file of one agent project
over tenon's line-delimited host protocol on stdin and stdout: one request
object per line in, one response object per line out. The protocol is tenon's
own, not MCP; the managed MCP boundary lives in tenon and speaks to this host,
so authors never write protocol code.

Authored contract: each visible ``tools/*.py`` file declares a module-level
``description`` string, Pydantic ``Input`` and ``Output`` models, and a
callable ``execute`` (sync or async). Pydantic resolves through the author's
own ``pyproject.toml``/``uv.lock``; tenon vendors nothing.

Every failure is answered in-band as a bounded ``error`` string. Nothing is
written to stderr on the request path: tenon never forwards host stderr into a
model-visible or operator-visible message.
"""

import asyncio
import importlib
import inspect
import json
import os
import site
import sys

# max_line_bytes bounds one request line, matching tenon's own bound.
max_line_bytes = 64 * 1024

# max_error_chars bounds one in-band error message.
max_error_chars = 1024

# required_shape is the authored contract, named in every violation.
required_shape = "description, Input, Output, and execute"


def bound(text):
    """Trim one message to a single bounded line."""
    flat = " ".join(str(text).split())
    if len(flat) > max_error_chars:
        return flat[:max_error_chars] + "..."
    return flat


def is_model(candidate):
    """Report whether candidate is a Pydantic BaseModel subclass."""
    from pydantic import BaseModel

    return isinstance(candidate, type) and issubclass(candidate, BaseModel)


def closed(schema):
    """Force additionalProperties False on every object node of a schema.

    An authored model that quietly accepts unknown keys would publish a
    surface wider than the one tenon validates, so the published schema states
    exactly what the host enforces.
    """
    if isinstance(schema, dict):
        if schema.get("type") == "object" or "properties" in schema:
            schema["additionalProperties"] = False
        for value in schema.values():
            closed(value)
    elif isinstance(schema, list):
        for value in schema:
            closed(value)
    return schema


def load_tools(source_dir):
    """Import every visible tools/*.py file and validate its module surface.

    Files whose name starts with ``_`` are shared helper modules: they are
    never imported as tools. A violation raises, naming the authored file and
    the required shape.
    """
    tools_dir = os.path.join(source_dir, "tools")
    files = sorted(
        name
        for name in os.listdir(tools_dir)
        if name.endswith(".py")
        and not name.startswith("_")
        and os.path.isfile(os.path.join(tools_dir, name))
    )

    tools = {}
    for filename in files:
        authored = "tools/" + filename
        stem = filename[: -len(".py")]
        try:
            module = importlib.import_module("tools." + stem)
        except Exception as error:  # noqa: BLE001 - reported in band, bounded
            raise RuntimeError(
                "%s could not be imported: %s" % (authored, error)
            ) from None

        description = getattr(module, "description", None)
        input_model = getattr(module, "Input", None)
        output_model = getattr(module, "Output", None)
        execute = getattr(module, "execute", None)
        if (
            not isinstance(description, str)
            or description.strip() == ""
            or not is_model(input_model)
            or not is_model(output_model)
            or not callable(execute)
        ):
            raise RuntimeError(
                "%s must export %s: description must be a non-empty string, "
                "Input and Output must be pydantic BaseModel subclasses, and "
                "execute must be callable" % (authored, required_shape)
            )

        # The filename supplies the tool name, with underscores exposed as
        # hyphens; tenon rejects any name it did not discover itself.
        name = stem.replace("_", "-")
        tools[name] = {
            "name": name,
            "description": description,
            "input": input_model,
            "output": output_model,
            "execute": execute,
        }
    return tools


def catalog(instance_id, tools):
    """Render the list result: every tool with both JSON Schemas."""
    listed = []
    for tool in tools.values():
        listed.append(
            {
                "name": tool["name"],
                "description": tool["description"],
                "inputSchema": closed(tool["input"].model_json_schema()),
                "outputSchema": closed(tool["output"].model_json_schema()),
            }
        )
    return {"instanceId": instance_id, "tools": listed}


def validate(model, value, surface):
    """Validate one JSON value against an authored model, extras forbidden."""
    if isinstance(value, model):
        return value
    if not isinstance(value, dict):
        raise RuntimeError("the %s must be a JSON object" % surface)
    unexpected = sorted(set(value) - set(model.model_fields))
    if unexpected:
        raise RuntimeError(
            "the %s carries unexpected fields: %s" % (surface, ", ".join(unexpected))
        )
    return model.model_validate(value, strict=True)


async def resolve(value):
    return await value


def invoke(instance_id, tools, request_id, params):
    """Validate arguments, run the tool, and validate its result."""
    name = (params or {}).get("name")
    if not isinstance(name, str) or name not in tools:
        raise RuntimeError("unknown tool")
    tool = tools[name]
    parsed = validate(tool["input"], (params or {}).get("arguments"), "arguments")
    result = tool["execute"](parsed, {"requestId": request_id})
    if inspect.isawaitable(result):
        result = asyncio.run(resolve(result))
    validated = validate(tool["output"], result, "tool output")
    return {"instanceId": instance_id, "output": validated.model_dump(mode="json")}


def write(value):
    sys.stdout.write(json.dumps(value) + "\n")
    sys.stdout.flush()


def main():
    if len(sys.argv) < 2:
        sys.exit(2)
    source_dir = sys.argv[1]
    # The closure carries no venv: the dependency directory locked at
    # preparation is added as a site directory here, at launch, so its
    # .pth files (namespace packages, editable-install shims) are honored
    # exactly as they would be inside a venv's site-packages.
    site_dir = os.environ.get("TENON_PYTHON_SITE")
    if site_dir:
        site.addsitedir(site_dir)
    sys.path.insert(0, source_dir)
    instance_id = "python:%d" % os.getpid()

    # Tools load once, at startup. A violation is remembered and answered in
    # band on every request, so the operator sees the authored file and the
    # required shape instead of a silent exit.
    tools = None
    load_error = None
    try:
        tools = load_tools(source_dir)
    except Exception as error:  # noqa: BLE001 - reported in band, bounded
        load_error = str(error)

    for raw in sys.stdin.buffer:
        if len(raw) > max_line_bytes:
            # A line over the bound is never truncated and never partially
            # interpreted: the host stops.
            sys.exit(1)
        line = raw.decode("utf-8", "replace").strip()
        if line == "":
            continue
        try:
            request = json.loads(line)
        except ValueError:
            write({"id": "", "error": "the request line is not one JSON object"})
            continue
        if not isinstance(request, dict):
            write({"id": "", "error": "the request line is not one JSON object"})
            continue
        request_id = request.get("id")
        if not isinstance(request_id, str):
            request_id = ""
        if load_error is not None:
            write({"id": request_id, "error": bound(load_error)})
            continue
        try:
            method = request.get("method")
            if method == "list":
                write({"id": request_id, "result": catalog(instance_id, tools)})
            elif method == "call":
                write(
                    {
                        "id": request_id,
                        "result": invoke(
                            instance_id, tools, request_id, request.get("params")
                        ),
                    }
                )
            else:
                write(
                    {
                        "id": request_id,
                        "error": "the host protocol supports list and call",
                    }
                )
        except Exception as error:  # noqa: BLE001 - reported in band, bounded
            write({"id": request_id, "error": bound(error)})


main()
