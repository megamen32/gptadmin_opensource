# Empty conftest for tests/mac — pytest still discovers tests/mac/launchd_verify.py
# without this file because of the project-root `tests/` conftest.py, but having
# a marker file here makes the directory explicitly part of the test suite.