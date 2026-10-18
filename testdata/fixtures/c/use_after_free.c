#include <stdlib.h>

int main(void) {
    int *block = malloc(2 * sizeof(int));
    if (block == NULL) {
        return 2;
    }
    block[0] = 7;
    free(block);
    return block[0]; /* use after free */
}
