#include <stdlib.h>
int main(void) { int *p = malloc(4 * sizeof(int)); p[4] = 7; return p[0]; }
