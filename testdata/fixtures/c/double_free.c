#include <stdlib.h>

int main(void) {
    int *block = malloc(2 * sizeof(int));
    if (block == NULL) {
        return 2;
    }
    free(block);
    free(block); /* double free */
    return 0;
}
