#include <stdlib.h>

int main(void) {
    int *block = malloc(4 * sizeof(int));
    if (block == NULL) {
        return 2;
    }
    block[9] = 42; /* out-of-bounds write */
    free(block);
    return 0;
}
