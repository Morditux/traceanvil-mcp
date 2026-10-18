#include <stdio.h>
#include <stdlib.h>

int main(void) {
    int *block = malloc(4 * sizeof(int));
    if (block == NULL) {
        return 2;
    }
    block[0] = 1;
    int value = block[7]; /* out-of-bounds read */
    printf("%d\n", value);
    free(block);
    return 0;
}
