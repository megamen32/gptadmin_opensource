# audit_imports.py
import ast, sys, importlib.util

try:
    STDLIB = set(sys.stdlib_module_names)  # Py3.10+
except AttributeError:
    # fallback: приблизительный набор, можно дополнить
    STDLIB = set()

def is_stdlib(mod: str) -> bool:
    if not mod:
        return True
    if mod.split(".")[0] in STDLIB:
        return True
    spec = importlib.util.find_spec(mod.split(".")[0])
    if spec is None or not spec.origin:
        return False
    return ("python" in spec.origin and "site-packages" not in spec.origin)

def collect(path: str):
    tree = ast.parse(open(path, "rb").read(), path)
    mods = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            for n in node.names:
                mods.add(n.name)
        elif isinstance(node, ast.ImportFrom):
            mods.add(node.module or "")
    return sorted(mods)

if __name__ == "__main__":
    import pathlib
    default_path = pathlib.Path(__file__).resolve().parents[1] / "services" / "hub_proxy.py"
    path = sys.argv[1] if len(sys.argv) > 1 else str(default_path)
    mods = collect(path)
    third = [m for m in mods if not is_stdlib(m)]
    print("All imports:", *mods, sep="\n  ")
    print("\nNon-stdlib:", *third, sep="\n  ")
