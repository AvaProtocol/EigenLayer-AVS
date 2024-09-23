/*
package trigger monitor the condition on when to fire a task
there are 3 trigger types:

Interval: Repeated at certain interval
Cron: trigger on time on cron
Onchain Event: when an event is emiited from a contract
Ev

# Storage Layout

Task is store into 2 storage
t:a:<task-id>: the raw json of task data
u:<task-id>: the task status
*/
package taskengine
