Разобрал алерт. Это не один сбой, а две группы.

## Быстрый recovery

В Forge UI вручную:

```
php artisan endpoints:refresh router_new_1_max_sym
php artisan endpoints:refresh rbtd
php artisan endpoints:refresh router_best_hubs_sym
php artisan endpoints:refresh multirouter_rbt_rbtd
```

Manual mode bypass'ит pause и schedule-gate, и сразу покажет реальную ошибку в stdout/stderr. Multirouter гонять последним — после успеха его двух источников.

Запускать сам не буду — нужен ваш ack для прод-команд. Если нужен план фикса, дайте `error_message` из audits-запроса.
