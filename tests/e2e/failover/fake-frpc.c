/* Controller-unit process double. This is not FRP or runtime acceptance.
 * Native executable identity lets production ownership checks remain strict.
 */
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>
#include <errno.h>

static volatile sig_atomic_t stopped;
static void stop(int signo) { (void)signo; stopped = 1; }

int main(int argc, char **argv) {
    (void)argv;
    const char *path = getenv("E2E_ROUTE_FILE");
    const char *value = getenv("E2E_ROUTE_VALUE");
    const char *runner = getenv("E2E_RUNNER_PID");
    if (argc != 3 || !path || !runner) return 2;
    char *end;
    long parent = strtol(runner, &end, 10);
    if (*end || parent <= 1) return 2;
    signal(SIGTERM, stop); signal(SIGINT, stop);
    FILE *f = fopen(path, "w");
    if (!f) return 3;
    fprintf(f, "%s\n", value ? value : "fallback");
    if (fclose(f)) return 3;
    while (!stopped && (kill((pid_t)parent, 0) == 0 || errno == EPERM)) sleep(1);
    return unlink(path) && errno != ENOENT ? 4 : 0;
}
