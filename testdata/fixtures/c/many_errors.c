#include <stdlib.h>

int main(void) {
    int *block = malloc(4 * sizeof(int));
    if (block == NULL) {
        return 2;
    }
    for (int i = 0; i < 2000; i++) {
        volatile int value = block[16 + (i % 8)];
        (void)value;
    }
    free(block);
    return 0;
}
