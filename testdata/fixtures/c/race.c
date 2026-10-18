#include <pthread.h>
#include <stdio.h>

static int shared_counter;

static void *worker(void *arg) {
    (void)arg;
    for (int i = 0; i < 1000; i++) {
        shared_counter++;
    }
    return NULL;
}

int main(void) {
    pthread_t first, second;
    pthread_create(&first, NULL, worker, NULL);
    pthread_create(&second, NULL, worker, NULL);
    pthread_join(first, NULL);
    pthread_join(second, NULL);
    printf("counter=%d\n", shared_counter);
    return 0;
}
