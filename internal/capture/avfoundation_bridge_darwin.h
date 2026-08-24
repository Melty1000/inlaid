#ifndef INLAID_AVFOUNDATION_BRIDGE_H
#define INLAID_AVFOUNDATION_BRIDGE_H

#include <stdint.h>

char *inlaid_avf_authorize(void);
char *inlaid_avf_devices_json(char **error_message);
char *inlaid_avf_modes_json(const char *device_id, char **error_message);

void *inlaid_avf_create(
    const char *device_id,
    int format_index,
    uint32_t source_subtype,
    int source_width,
    int source_height,
    int64_t frame_duration_value,
    int32_t frame_duration_timescale,
    int output_width,
    int output_height,
    int allow_variable_frame_rate,
    uintptr_t go_handle,
    char **error_message);

char *inlaid_avf_start(void *capture);
int inlaid_avf_close(void *capture, int64_t timeout_milliseconds, char **error_message);
void inlaid_avf_frame_release(void *frame);
void inlaid_avf_free_string(char *value);

#endif
