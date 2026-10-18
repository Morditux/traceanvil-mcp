int main(void) {
    volatile int *pointer = 0;
    *pointer = 1; /* null write */
    return 0;
}
