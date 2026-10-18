#include <stdlib.h>

static void *reachable;

int main(void) {
    reachable = malloc(16); /* still reachable at exit */
    void *lost = malloc(8); /* definitely lost */
    if (reachable == NULL || lost == NULL) {
        return 2;
    }
    lost = NULL;
    return 0;
}
