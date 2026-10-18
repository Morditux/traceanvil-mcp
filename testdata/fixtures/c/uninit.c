#include <stdio.h>

int main(void) {
    int value;
    if (value == 42) { /* conditional jump on an uninitialised value */
        printf("unreachable\n");
    }
    return 0;
}
