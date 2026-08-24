//go:build linux && cgo

package capture

/*
#cgo pkg-config: libturbojpeg
#ifndef _GNU_SOURCE
#define _GNU_SOURCE
#endif
#include <errno.h>
#include <fcntl.h>
#include <linux/videodev2.h>
#include <poll.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <sys/eventfd.h>
#include <sys/ioctl.h>
#include <sys/mman.h>
#include <unistd.h>
#include <turbojpeg.h>

enum {
	INLAID_V4L2_PIX_FMT_MJPEG = V4L2_PIX_FMT_MJPEG,
	INLAID_V4L2_FMT_FLAG_EMULATED = V4L2_FMT_FLAG_EMULATED
};

typedef struct {
	uint32_t width;
	uint32_t height;
	uint32_t fps_numerator;
	uint32_t fps_denominator;
	uint32_t pixel_format;
	uint32_t buffer_type;
	uint32_t format_flags;
	uint32_t ordinal;
} inlaid_mode;

typedef struct {
	void *data;
	size_t length;
	off_t offset;
} inlaid_map;

typedef struct {
	int fd;
	int wake_fd;
	enum v4l2_buf_type type;
	inlaid_map *maps;
	uint32_t count;
	int buffers_requested;
	int streaming;
} inlaid_stream;

typedef struct {
	void *data;
	size_t mapped_length;
	size_t bytes_used;
	size_t data_offset;
	uint32_t index;
	uint32_t sequence;
	uint32_t flags;
	int64_t seconds;
	int64_t microseconds;
} inlaid_sample;

typedef struct {
	tjhandle handle;
} inlaid_jpeg;

typedef struct {
	int image_width;
	int image_height;
	int y_width;
	int y_height;
	int cb_width;
	int cb_height;
	int cr_width;
	int cr_height;
	int subsampling;
} inlaid_jpeg_layout;

static int inlaid_ioctl(int fd, unsigned long request, void *value) {
	int result;
	do {
		result = ioctl(fd, request, value);
	} while (result < 0 && errno == EINTR);
	return result;
}

static int inlaid_errno(void) {
	return errno == 0 ? -EIO : -errno;
}

static int inlaid_reserve_map_bytes(size_t length, uint64_t max_buffer_bytes,
	uint64_t max_mapped_bytes, uint64_t *mapped_bytes) {
	if (length == 0 || max_buffer_bytes == 0 || max_mapped_bytes == 0 || mapped_bytes == NULL) {
		return -EINVAL;
	}
	uint64_t bytes = (uint64_t)length;
	if (bytes > max_buffer_bytes || *mapped_bytes > max_mapped_bytes ||
		bytes > max_mapped_bytes - *mapped_bytes) {
		return -EOVERFLOW;
	}
	*mapped_bytes += bytes;
	return 0;
}

static uint32_t inlaid_capabilities(const struct v4l2_capability *capability) {
	if (capability->capabilities & V4L2_CAP_DEVICE_CAPS) {
		return capability->device_caps;
	}
	return capability->capabilities;
}

static int inlaid_query_device(int fd, char *card, size_t card_size, uint32_t *buffer_type) {
	struct v4l2_capability capability;
	memset(&capability, 0, sizeof(capability));
	if (inlaid_ioctl(fd, VIDIOC_QUERYCAP, &capability) < 0) {
		return inlaid_errno();
	}
	uint32_t caps = inlaid_capabilities(&capability);
	if (!(caps & V4L2_CAP_STREAMING)) {
		return -ENOTSUP;
	}
	if (caps & V4L2_CAP_VIDEO_CAPTURE) {
		*buffer_type = V4L2_BUF_TYPE_VIDEO_CAPTURE;
	} else if (caps & V4L2_CAP_VIDEO_CAPTURE_MPLANE) {
		*buffer_type = V4L2_BUF_TYPE_VIDEO_CAPTURE_MPLANE;
	} else {
		return -ENOTSUP;
	}
	if (card != NULL && card_size > 0) {
		size_t length = strnlen((const char *)capability.card, sizeof(capability.card));
		if (length >= card_size) {
			length = card_size - 1;
		}
		memcpy(card, capability.card, length);
		card[length] = '\0';
	}
	return 0;
}

static int inlaid_open_probe(const char *path, char *card, size_t card_size, int *fd, uint32_t *buffer_type) {
	int opened = open(path, O_RDWR | O_NONBLOCK | O_CLOEXEC);
	if (opened < 0) {
		return inlaid_errno();
	}
	int result = inlaid_query_device(opened, card, card_size, buffer_type);
	if (result < 0) {
		close(opened);
		return result;
	}
	*fd = opened;
	return 0;
}

static uint32_t inlaid_clamp_step(uint32_t requested, uint32_t minimum, uint32_t maximum, uint32_t step) {
	if (requested <= minimum) {
		return minimum;
	}
	if (requested >= maximum) {
		return maximum;
	}
	if (step == 0) {
		return requested;
	}
	uint64_t offset = requested - minimum;
	uint64_t rounded = (offset + step / 2) / step;
	uint64_t value = (uint64_t)minimum + rounded * step;
	return value > maximum ? maximum : (uint32_t)value;
}

static void inlaid_emit_mode(inlaid_mode *modes, uint32_t capacity, uint32_t *total,
	uint32_t width, uint32_t height, uint32_t fps_numerator, uint32_t fps_denominator,
	uint32_t pixel_format, uint32_t buffer_type, uint32_t format_flags, uint32_t ordinal) {
	if (width == 0 || height == 0 || fps_numerator == 0 || fps_denominator == 0) {
		return;
	}
	uint32_t index = *total;
	if (index < capacity) {
		modes[index].width = width;
		modes[index].height = height;
		modes[index].fps_numerator = fps_numerator;
		modes[index].fps_denominator = fps_denominator;
		modes[index].pixel_format = pixel_format;
		modes[index].buffer_type = buffer_type;
		modes[index].format_flags = format_flags;
		modes[index].ordinal = ordinal;
	}
	*total = index + 1;
}

static int inlaid_emit_intervals(int fd, uint32_t pixel_format, uint32_t buffer_type,
	uint32_t format_flags, uint32_t width, uint32_t height, uint32_t requested_fps,
	uint32_t ordinal, inlaid_mode *modes, uint32_t capacity, uint32_t *total) {
	struct v4l2_frmivalenum interval;
	memset(&interval, 0, sizeof(interval));
	interval.pixel_format = pixel_format;
	interval.width = width;
	interval.height = height;
	int found = 0;
	for (interval.index = 0;; interval.index++) {
		if (inlaid_ioctl(fd, VIDIOC_ENUM_FRAMEINTERVALS, &interval) < 0) {
			if (errno == EINVAL) {
				break;
			}
			return inlaid_errno();
		}
		found = 1;
		if (interval.type == V4L2_FRMIVAL_TYPE_DISCRETE) {
			inlaid_emit_mode(modes, capacity, total, width, height,
				interval.discrete.denominator, interval.discrete.numerator,
				pixel_format, buffer_type, format_flags, ordinal + interval.index);
			continue;
		}
		inlaid_emit_mode(modes, capacity, total, width, height, requested_fps, 1,
			pixel_format, buffer_type, format_flags, ordinal + interval.index);
		inlaid_emit_mode(modes, capacity, total, width, height,
			interval.stepwise.min.denominator, interval.stepwise.min.numerator,
			pixel_format, buffer_type, format_flags, ordinal + interval.index + 1);
		inlaid_emit_mode(modes, capacity, total, width, height,
			interval.stepwise.max.denominator, interval.stepwise.max.numerator,
			pixel_format, buffer_type, format_flags, ordinal + interval.index + 2);
		break;
	}
	if (!found) {
		inlaid_emit_mode(modes, capacity, total, width, height, requested_fps, 1,
			pixel_format, buffer_type, format_flags, ordinal);
	}
	return 0;
}

static int inlaid_enumerate_modes(int fd, uint32_t buffer_type, uint32_t requested_width,
	uint32_t requested_height, uint32_t requested_fps, inlaid_mode *modes,
	uint32_t capacity, uint32_t *total) {
	*total = 0;
	uint32_t ordinal = 0;
	struct v4l2_fmtdesc format;
	memset(&format, 0, sizeof(format));
	format.type = buffer_type;
	for (format.index = 0;; format.index++) {
		if (inlaid_ioctl(fd, VIDIOC_ENUM_FMT, &format) < 0) {
			if (errno == EINVAL) {
				break;
			}
			return inlaid_errno();
		}
		if (format.pixelformat != V4L2_PIX_FMT_MJPEG) {
			continue;
		}
		struct v4l2_frmsizeenum size;
		memset(&size, 0, sizeof(size));
		size.pixel_format = format.pixelformat;
		int found_size = 0;
		for (size.index = 0;; size.index++) {
			if (inlaid_ioctl(fd, VIDIOC_ENUM_FRAMESIZES, &size) < 0) {
				if (errno == EINVAL) {
					break;
				}
				return inlaid_errno();
			}
			found_size = 1;
			if (size.type == V4L2_FRMSIZE_TYPE_DISCRETE) {
				int result = inlaid_emit_intervals(fd, format.pixelformat, buffer_type,
					format.flags, size.discrete.width, size.discrete.height, requested_fps,
					ordinal, modes, capacity, total);
				if (result < 0) {
					return result;
				}
				ordinal += 1024;
				continue;
			}
			uint32_t widths[3] = {
				inlaid_clamp_step(requested_width, size.stepwise.min_width,
					size.stepwise.max_width, size.stepwise.step_width),
				size.stepwise.min_width,
				size.stepwise.max_width
			};
			uint32_t heights[3] = {
				inlaid_clamp_step(requested_height, size.stepwise.min_height,
					size.stepwise.max_height, size.stepwise.step_height),
				size.stepwise.min_height,
				size.stepwise.max_height
			};
			for (int candidate = 0; candidate < 3; candidate++) {
				if (candidate > 0 && widths[candidate] == widths[0] && heights[candidate] == heights[0]) {
					continue;
				}
				int result = inlaid_emit_intervals(fd, format.pixelformat, buffer_type,
					format.flags, widths[candidate], heights[candidate], requested_fps,
					ordinal, modes, capacity, total);
				if (result < 0) {
					return result;
				}
				ordinal += 1024;
			}
			break;
		}
		if (!found_size) {
			int result = inlaid_emit_intervals(fd, format.pixelformat, buffer_type,
				format.flags, requested_width, requested_height, requested_fps,
				ordinal, modes, capacity, total);
			if (result < 0) {
				return result;
			}
		}
	}
	return 0;
}

static int inlaid_configure(int fd, const inlaid_mode *requested, inlaid_mode *actual) {
	struct v4l2_format format;
	memset(&format, 0, sizeof(format));
	format.type = requested->buffer_type;
	if (format.type == V4L2_BUF_TYPE_VIDEO_CAPTURE_MPLANE) {
		format.fmt.pix_mp.width = requested->width;
		format.fmt.pix_mp.height = requested->height;
		format.fmt.pix_mp.pixelformat = requested->pixel_format;
		format.fmt.pix_mp.field = V4L2_FIELD_ANY;
	} else {
		format.fmt.pix.width = requested->width;
		format.fmt.pix.height = requested->height;
		format.fmt.pix.pixelformat = requested->pixel_format;
		format.fmt.pix.field = V4L2_FIELD_ANY;
	}
	if (inlaid_ioctl(fd, VIDIOC_S_FMT, &format) < 0) {
		return inlaid_errno();
	}
	uint32_t width;
	uint32_t height;
	uint32_t pixel_format;
	if (format.type == V4L2_BUF_TYPE_VIDEO_CAPTURE_MPLANE) {
		width = format.fmt.pix_mp.width;
		height = format.fmt.pix_mp.height;
		pixel_format = format.fmt.pix_mp.pixelformat;
	} else {
		width = format.fmt.pix.width;
		height = format.fmt.pix.height;
		pixel_format = format.fmt.pix.pixelformat;
	}
	if (pixel_format != requested->pixel_format) {
		return -ENOTSUP;
	}

	struct v4l2_streamparm parameters;
	memset(&parameters, 0, sizeof(parameters));
	parameters.type = requested->buffer_type;
	parameters.parm.capture.timeperframe.numerator = requested->fps_denominator;
	parameters.parm.capture.timeperframe.denominator = requested->fps_numerator;
	uint32_t interval_numerator = 0;
	uint32_t interval_denominator = 0;
	if (inlaid_ioctl(fd, VIDIOC_S_PARM, &parameters) < 0) {
		if (errno != EINVAL) {
			return inlaid_errno();
		}
	} else if (parameters.parm.capture.timeperframe.numerator != 0 &&
		parameters.parm.capture.timeperframe.denominator != 0) {
		interval_numerator = parameters.parm.capture.timeperframe.numerator;
		interval_denominator = parameters.parm.capture.timeperframe.denominator;
	}
	memset(&parameters, 0, sizeof(parameters));
	parameters.type = requested->buffer_type;
	if (inlaid_ioctl(fd, VIDIOC_G_PARM, &parameters) == 0 &&
		parameters.parm.capture.timeperframe.numerator != 0 &&
		parameters.parm.capture.timeperframe.denominator != 0) {
		interval_numerator = parameters.parm.capture.timeperframe.numerator;
		interval_denominator = parameters.parm.capture.timeperframe.denominator;
	}
	if (interval_numerator == 0 || interval_denominator == 0) {
		return -ENODATA;
	}
	memset(actual, 0, sizeof(*actual));
	actual->width = width;
	actual->height = height;
	actual->fps_numerator = interval_denominator;
	actual->fps_denominator = interval_numerator;
	actual->pixel_format = pixel_format;
	actual->buffer_type = requested->buffer_type;
	actual->format_flags = requested->format_flags;
	actual->ordinal = requested->ordinal;
	return 0;
}

static int inlaid_release_stream(inlaid_stream *stream) {
	if (stream == NULL) {
		return 0;
	}
	int first_error = 0;
	if (stream->streaming && stream->fd >= 0) {
		enum v4l2_buf_type type = stream->type;
		if (inlaid_ioctl(stream->fd, VIDIOC_STREAMOFF, &type) < 0 && first_error == 0) {
			first_error = inlaid_errno();
		}
		stream->streaming = 0;
	}
	if (stream->maps != NULL) {
		for (uint32_t index = 0; index < stream->count; index++) {
			if (stream->maps[index].data != NULL && stream->maps[index].data != MAP_FAILED) {
				if (munmap(stream->maps[index].data, stream->maps[index].length) < 0 && first_error == 0) {
					first_error = inlaid_errno();
				}
			}
		}
		free(stream->maps);
	}
	if (stream->fd >= 0 && stream->buffers_requested) {
		struct v4l2_requestbuffers request;
		memset(&request, 0, sizeof(request));
		request.type = stream->type;
		request.memory = V4L2_MEMORY_MMAP;
		request.count = 0;
		if (inlaid_ioctl(stream->fd, VIDIOC_REQBUFS, &request) < 0 && first_error == 0 && errno != EINVAL) {
			first_error = inlaid_errno();
		}
		stream->buffers_requested = 0;
	}
	if (stream->fd >= 0) {
		if (close(stream->fd) < 0 && first_error == 0) {
			first_error = inlaid_errno();
		}
	}
	if (stream->wake_fd >= 0) {
		if (close(stream->wake_fd) < 0 && first_error == 0) {
			first_error = inlaid_errno();
		}
	}
	free(stream);
	return first_error;
}

static int inlaid_prepare_buffer(inlaid_stream *stream, uint32_t index, struct v4l2_buffer *buffer,
	struct v4l2_plane *planes) {
	memset(buffer, 0, sizeof(*buffer));
	buffer->type = stream->type;
	buffer->memory = V4L2_MEMORY_MMAP;
	buffer->index = index;
	if (stream->type == V4L2_BUF_TYPE_VIDEO_CAPTURE_MPLANE) {
		memset(planes, 0, sizeof(struct v4l2_plane) * VIDEO_MAX_PLANES);
		buffer->m.planes = planes;
		buffer->length = VIDEO_MAX_PLANES;
	}
	return 0;
}

static int inlaid_start_stream(const char *path, const inlaid_mode *requested,
	uint32_t requested_buffers, uint64_t max_buffer_bytes, uint64_t max_mapped_bytes,
	inlaid_mode *actual, inlaid_stream **output) {
	*output = NULL;
	inlaid_stream *stream = calloc(1, sizeof(*stream));
	if (stream == NULL) {
		return -ENOMEM;
	}
	stream->fd = -1;
	stream->wake_fd = -1;
	stream->type = requested->buffer_type;
	stream->fd = open(path, O_RDWR | O_NONBLOCK | O_CLOEXEC);
	if (stream->fd < 0) {
		int result = inlaid_errno();
		inlaid_release_stream(stream);
		return result;
	}
	uint32_t discovered_type = 0;
	int result = inlaid_query_device(stream->fd, NULL, 0, &discovered_type);
	if (result < 0 || discovered_type != requested->buffer_type) {
		inlaid_release_stream(stream);
		return result < 0 ? result : -ENOTSUP;
	}
	result = inlaid_configure(stream->fd, requested, actual);
	if (result < 0) {
		inlaid_release_stream(stream);
		return result;
	}
	if (actual->width != requested->width || actual->height != requested->height ||
		actual->pixel_format != requested->pixel_format ||
		(uint64_t)actual->fps_numerator * requested->fps_denominator !=
		(uint64_t)requested->fps_numerator * actual->fps_denominator) {
		inlaid_release_stream(stream);
		return -ERANGE;
	}

	struct v4l2_requestbuffers request;
	memset(&request, 0, sizeof(request));
	request.type = stream->type;
	request.memory = V4L2_MEMORY_MMAP;
	request.count = requested_buffers;
	if (inlaid_ioctl(stream->fd, VIDIOC_REQBUFS, &request) < 0) {
		result = inlaid_errno();
		inlaid_release_stream(stream);
		return result;
	}
	stream->buffers_requested = 1;
	if (request.count < 2 || request.count > 8) {
		inlaid_release_stream(stream);
		return -ENOBUFS;
	}
	stream->maps = calloc(request.count, sizeof(*stream->maps));
	if (stream->maps == NULL) {
		inlaid_release_stream(stream);
		return -ENOMEM;
	}
	stream->count = request.count;
	uint64_t mapped_bytes = 0;
	for (uint32_t index = 0; index < stream->count; index++) {
		struct v4l2_buffer buffer;
		struct v4l2_plane planes[VIDEO_MAX_PLANES];
		inlaid_prepare_buffer(stream, index, &buffer, planes);
		if (inlaid_ioctl(stream->fd, VIDIOC_QUERYBUF, &buffer) < 0) {
			result = inlaid_errno();
			inlaid_release_stream(stream);
			return result;
		}
		size_t length;
		off_t offset;
		if (stream->type == V4L2_BUF_TYPE_VIDEO_CAPTURE_MPLANE) {
			if (buffer.length != 1 || planes[0].length == 0) {
				inlaid_release_stream(stream);
				return -ENOTSUP;
			}
			length = planes[0].length;
			offset = planes[0].m.mem_offset;
		} else {
			length = buffer.length;
			offset = buffer.m.offset;
		}
		result = inlaid_reserve_map_bytes(length, max_buffer_bytes, max_mapped_bytes, &mapped_bytes);
		if (result < 0) {
			inlaid_release_stream(stream);
			return result;
		}
		stream->maps[index].length = length;
		stream->maps[index].offset = offset;
	}
	for (uint32_t index = 0; index < stream->count; index++) {
		struct v4l2_buffer buffer;
		struct v4l2_plane planes[VIDEO_MAX_PLANES];
		size_t length = stream->maps[index].length;
		stream->maps[index].data = mmap(NULL, length, PROT_READ | PROT_WRITE,
			MAP_SHARED, stream->fd, stream->maps[index].offset);
		if (stream->maps[index].data == MAP_FAILED) {
			result = inlaid_errno();
			stream->maps[index].data = NULL;
			inlaid_release_stream(stream);
			return result;
		}
		inlaid_prepare_buffer(stream, index, &buffer, planes);
		if (stream->type == V4L2_BUF_TYPE_VIDEO_CAPTURE_MPLANE) {
			buffer.length = 1;
			planes[0].length = length;
		}
		if (inlaid_ioctl(stream->fd, VIDIOC_QBUF, &buffer) < 0) {
			result = inlaid_errno();
			inlaid_release_stream(stream);
			return result;
		}
	}
	stream->wake_fd = eventfd(0, EFD_NONBLOCK | EFD_CLOEXEC);
	if (stream->wake_fd < 0) {
		result = inlaid_errno();
		inlaid_release_stream(stream);
		return result;
	}
	if (inlaid_ioctl(stream->fd, VIDIOC_STREAMON, &stream->type) < 0) {
		result = inlaid_errno();
		inlaid_release_stream(stream);
		return result;
	}
	stream->streaming = 1;
	*output = stream;
	return 0;
}

static int inlaid_next_sample(inlaid_stream *stream, int timeout_ms, inlaid_sample *sample) {
	struct pollfd descriptors[2];
	memset(descriptors, 0, sizeof(descriptors));
	descriptors[0].fd = stream->fd;
	descriptors[0].events = POLLIN | POLLPRI;
	descriptors[1].fd = stream->wake_fd;
	descriptors[1].events = POLLIN;
	int ready;
	do {
		ready = poll(descriptors, 2, timeout_ms);
	} while (ready < 0 && errno == EINTR);
	if (ready < 0) {
		return inlaid_errno();
	}
	if (ready == 0) {
		return 1;
	}
	if (descriptors[1].revents & POLLIN) {
		uint64_t value;
		while (read(stream->wake_fd, &value, sizeof(value)) < 0 && errno == EINTR) {}
		return 2;
	}
	if (descriptors[0].revents & (POLLHUP | POLLNVAL)) {
		return -ENODEV;
	}
	if ((descriptors[0].revents & POLLERR) &&
		!(descriptors[0].revents & (POLLIN | POLLPRI))) {
		return -EIO;
	}
	struct v4l2_buffer buffer;
	struct v4l2_plane planes[VIDEO_MAX_PLANES];
	inlaid_prepare_buffer(stream, 0, &buffer, planes);
	if (inlaid_ioctl(stream->fd, VIDIOC_DQBUF, &buffer) < 0) {
		if (errno == EAGAIN) {
			return 1;
		}
		return inlaid_errno();
	}
	if (buffer.index >= stream->count) {
		return -EIO;
	}
	size_t bytes_used = buffer.bytesused;
	size_t data_offset = 0;
	if (stream->type == V4L2_BUF_TYPE_VIDEO_CAPTURE_MPLANE) {
		bytes_used = planes[0].bytesused;
		data_offset = planes[0].data_offset;
	}
	memset(sample, 0, sizeof(*sample));
	sample->data = stream->maps[buffer.index].data;
	sample->mapped_length = stream->maps[buffer.index].length;
	sample->bytes_used = bytes_used;
	sample->data_offset = data_offset;
	sample->index = buffer.index;
	sample->sequence = buffer.sequence;
	sample->flags = buffer.flags;
	sample->seconds = buffer.timestamp.tv_sec;
	sample->microseconds = buffer.timestamp.tv_usec;
	return 0;
}

static int inlaid_requeue(inlaid_stream *stream, uint32_t index) {
	if (index >= stream->count) {
		return -EINVAL;
	}
	struct v4l2_buffer buffer;
	struct v4l2_plane planes[VIDEO_MAX_PLANES];
	inlaid_prepare_buffer(stream, index, &buffer, planes);
	if (stream->type == V4L2_BUF_TYPE_VIDEO_CAPTURE_MPLANE) {
		buffer.length = 1;
		planes[0].length = stream->maps[index].length;
	}
	if (inlaid_ioctl(stream->fd, VIDIOC_QBUF, &buffer) < 0) {
		return inlaid_errno();
	}
	return 0;
}

static int inlaid_wake_stream(inlaid_stream *stream) {
	uint64_t value = 1;
	ssize_t written;
	do {
		written = write(stream->wake_fd, &value, sizeof(value));
	} while (written < 0 && errno == EINTR);
	if (written < 0 && errno != EAGAIN) {
		return inlaid_errno();
	}
	return 0;
}

static int inlaid_sample_has_error(const inlaid_sample *sample) {
	return (sample->flags & V4L2_BUF_FLAG_ERROR) != 0;
}

static int inlaid_sample_is_monotonic(const inlaid_sample *sample) {
	return (sample->flags & V4L2_BUF_FLAG_TIMESTAMP_MASK) == V4L2_BUF_FLAG_TIMESTAMP_MONOTONIC;
}

static int inlaid_jpeg_create(inlaid_jpeg **output) {
	*output = calloc(1, sizeof(inlaid_jpeg));
	if (*output == NULL) {
		return -ENOMEM;
	}
	(*output)->handle = tjInitDecompress();
	if ((*output)->handle == NULL) {
		free(*output);
		*output = NULL;
		return -EIO;
	}
	return 0;
}

static void inlaid_jpeg_destroy(inlaid_jpeg *decoder) {
	if (decoder == NULL) {
		return;
	}
	if (decoder->handle != NULL) {
		tjDestroy(decoder->handle);
	}
	free(decoder);
}

static const char *inlaid_jpeg_error(inlaid_jpeg *decoder) {
	return decoder == NULL ? "TurboJPEG decoder is unavailable" : tjGetErrorStr2(decoder->handle);
}

static int inlaid_jpeg_layout_for(inlaid_jpeg *decoder, const unsigned char *jpeg,
	unsigned long length, int source_width, int source_height, int downsample,
	inlaid_jpeg_layout *layout) {
	int width = 0;
	int height = 0;
	int subsampling = 0;
	int colorspace = 0;
	if (tjDecompressHeader3(decoder->handle, jpeg, length, &width, &height,
		&subsampling, &colorspace) < 0) {
		return -1;
	}
	if (width != source_width || height != source_height || colorspace != TJCS_YCbCr ||
		subsampling == TJSAMP_GRAY) {
		return -2;
	}
	int factor_count = 0;
	tjscalingfactor *factors = tjGetScalingFactors(&factor_count);
	if (factors == NULL) {
		return -1;
	}
	int scaled_width = 0;
	int scaled_height = 0;
	for (int index = 0; index < factor_count; index++) {
		if (factors[index].num == 1 && factors[index].denom == downsample) {
			scaled_width = TJSCALED(width, factors[index]);
			scaled_height = TJSCALED(height, factors[index]);
			break;
		}
	}
	if (scaled_width <= 0 || scaled_height <= 0) {
		return -3;
	}
	layout->image_width = scaled_width;
	layout->image_height = scaled_height;
	layout->y_width = tjPlaneWidth(0, scaled_width, subsampling);
	layout->y_height = tjPlaneHeight(0, scaled_height, subsampling);
	layout->cb_width = tjPlaneWidth(1, scaled_width, subsampling);
	layout->cb_height = tjPlaneHeight(1, scaled_height, subsampling);
	layout->cr_width = tjPlaneWidth(2, scaled_width, subsampling);
	layout->cr_height = tjPlaneHeight(2, scaled_height, subsampling);
	layout->subsampling = subsampling;
	if (layout->y_width <= 0 || layout->y_height <= 0 || layout->cb_width <= 0 ||
		layout->cb_height <= 0 || layout->cr_width <= 0 || layout->cr_height <= 0) {
		return -2;
	}
	return 0;
}

static int inlaid_jpeg_decode(inlaid_jpeg *decoder, const unsigned char *jpeg,
	unsigned long length, unsigned char *y, int y_stride, unsigned char *cb,
	int cb_stride, unsigned char *cr, int cr_stride, int width, int height) {
	unsigned char *planes[3] = { y, cb, cr };
	int strides[3] = { y_stride, cb_stride, cr_stride };
	return tjDecompressToYUVPlanes(decoder->handle, jpeg, length, planes,
		width, strides, height, 0);
}
*/
import "C"

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	linuxMJPEG          = uint32(C.INLAID_V4L2_PIX_FMT_MJPEG)
	linuxFormatEmulated = uint32(C.INLAID_V4L2_FMT_FLAG_EMULATED)
)

type linuxNativeMode struct {
	Mode
	bufferType uint32
	formatFlag uint32
	ordinal    uint32
}

type linuxNativeSample struct {
	data         unsafe.Pointer
	bytesUsed    int
	index        uint32
	sequence     uint32
	seconds      int64
	microseconds int64
	monotonic    bool
	damaged      bool
}

type linuxNativeStream struct {
	value *C.inlaid_stream
}

type linuxJPEGLayout struct {
	imageWidth, imageHeight int
	yWidth, yHeight         int
	cbWidth, cbHeight       int
	crWidth, crHeight       int
}

type linuxJPEGDecoder struct {
	value *C.inlaid_jpeg
}

func linuxNativeAvailable() bool { return true }

func linuxResult(operation, path string, result C.int) error {
	if result >= 0 {
		return nil
	}
	errno := syscall.Errno(-int(result))
	switch errno {
	case syscall.EACCES, syscall.EPERM:
		return fmt.Errorf("%s: camera permission denied for %s: %w", operation, path, errno)
	case syscall.EBUSY:
		return fmt.Errorf("%s: camera is busy at %s: %w", operation, path, errno)
	case syscall.ENODEV, syscall.ENXIO, syscall.ENOENT, syscall.EPIPE:
		return fmt.Errorf("%s: camera disconnected at %s: %w", operation, path, errno)
	case syscall.EOPNOTSUPP:
		return fmt.Errorf("%s: camera does not support native MJPEG streaming at %s: %w", operation, path, errno)
	case syscall.ENODATA:
		return fmt.Errorf("%s: camera driver did not report a verifiable actual frame rate for %s: %w", operation, path, errno)
	case syscall.EOVERFLOW:
		return fmt.Errorf("%s: camera buffer mapping exceeds the configured packet memory budget at %s: %w", operation, path, errno)
	default:
		return fmt.Errorf("%s %s: %w", operation, path, errno)
	}
}

func nativeProbe(path string) (int, string, uint32, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	card := make([]byte, 256)
	var fd C.int
	var bufferType C.uint32_t
	result := C.inlaid_open_probe(cPath, (*C.char)(unsafe.Pointer(&card[0])), C.size_t(len(card)), &fd, &bufferType)
	if err := linuxResult("probe camera", path, result); err != nil {
		return -1, "", 0, err
	}
	name := string(card)
	if zero := indexByte(card, 0); zero >= 0 {
		name = string(card[:zero])
	}
	return int(fd), name, uint32(bufferType), nil
}

func nativeCloseFD(fd int) {
	if fd >= 0 {
		C.close(C.int(fd))
	}
}

func nativeEnumerateModes(fd int, bufferType uint32, cfg Config) ([]linuxNativeMode, error) {
	capacity := 128
	for {
		if capacity > 4096 {
			return nil, errors.New("camera exposed more than 4096 native modes")
		}
		storage := make([]C.inlaid_mode, capacity)
		var total C.uint32_t
		result := C.inlaid_enumerate_modes(C.int(fd), C.uint32_t(bufferType), C.uint32_t(cfg.Width),
			C.uint32_t(cfg.Height), C.uint32_t(cfg.FPS), &storage[0], C.uint32_t(capacity), &total)
		if err := linuxResult("enumerate native modes", "camera", result); err != nil {
			return nil, err
		}
		if int(total) > capacity {
			capacity = int(total)
			continue
		}
		modes := make([]linuxNativeMode, 0, int(total))
		for _, item := range storage[:int(total)] {
			modes = append(modes, nativeModeFromC(item))
		}
		return modes, nil
	}
}

func nativeConfigure(fd int, requested linuxNativeMode) (linuxNativeMode, error) {
	native := nativeModeToC(requested)
	var actual C.inlaid_mode
	result := C.inlaid_configure(C.int(fd), &native, &actual)
	if err := linuxResult("configure native mode", "camera", result); err != nil {
		return linuxNativeMode{}, err
	}
	return nativeModeFromC(actual), nil
}

func nativeStart(path string, requested linuxNativeMode, buffers, maxBufferBytes, maxMappedBytes int) (*linuxNativeStream, linuxNativeMode, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	native := nativeModeToC(requested)
	var actual C.inlaid_mode
	var stream *C.inlaid_stream
	result := C.inlaid_start_stream(cPath, &native, C.uint32_t(buffers), C.uint64_t(maxBufferBytes),
		C.uint64_t(maxMappedBytes), &actual, &stream)
	if err := linuxResult("start native stream", path, result); err != nil {
		return nil, linuxNativeMode{}, err
	}
	return &linuxNativeStream{value: stream}, nativeModeFromC(actual), nil
}

func nativeMapBudget(lengths []uint64, maxBufferBytes, maxMappedBytes uint64) (uint64, error) {
	var mappedBytes C.uint64_t
	for _, length := range lengths {
		result := C.inlaid_reserve_map_bytes(C.size_t(length), C.uint64_t(maxBufferBytes),
			C.uint64_t(maxMappedBytes), &mappedBytes)
		if err := linuxResult("validate native buffer budget", "camera", result); err != nil {
			return uint64(mappedBytes), err
		}
	}
	return uint64(mappedBytes), nil
}

func (s *linuxNativeStream) next(timeoutMilliseconds int) (linuxNativeSample, int, error) {
	var sample C.inlaid_sample
	result := C.inlaid_next_sample(s.value, C.int(timeoutMilliseconds), &sample)
	if result == 1 || result == 2 {
		return linuxNativeSample{}, int(result), nil
	}
	if err := linuxResult("read native frame", "camera", result); err != nil {
		return linuxNativeSample{}, 0, err
	}
	offset, payloadBytes, err := linuxPayloadBounds(uint64(sample.mapped_length), uint64(sample.bytes_used), uint64(sample.data_offset))
	if err != nil {
		return linuxNativeSample{}, 0, err
	}
	return linuxNativeSample{
		data:         unsafe.Add(sample.data, offset),
		bytesUsed:    payloadBytes,
		index:        uint32(sample.index),
		sequence:     uint32(sample.sequence),
		seconds:      int64(sample.seconds),
		microseconds: int64(sample.microseconds),
		monotonic:    C.inlaid_sample_is_monotonic(&sample) != 0,
		damaged:      C.inlaid_sample_has_error(&sample) != 0,
	}, 0, nil
}

func (s *linuxNativeStream) copySample(sample linuxNativeSample, destination []byte) {
	copy(destination, unsafe.Slice((*byte)(sample.data), sample.bytesUsed))
	runtime.KeepAlive(s)
}

func (s *linuxNativeStream) requeue(index uint32) error {
	return linuxResult("requeue native frame", "camera", C.inlaid_requeue(s.value, C.uint32_t(index)))
}

func (s *linuxNativeStream) wake() error {
	return linuxResult("wake native stream", "camera", C.inlaid_wake_stream(s.value))
}

func (s *linuxNativeStream) close() error {
	if s != nil && s.value != nil {
		result := C.inlaid_release_stream(s.value)
		s.value = nil
		return linuxResult("stop native stream", "camera", result)
	}
	return nil
}

func newLinuxJPEGDecoder() (*linuxJPEGDecoder, error) {
	var decoder *C.inlaid_jpeg
	if result := C.inlaid_jpeg_create(&decoder); result < 0 {
		return nil, linuxResult("create TurboJPEG decoder", "camera", result)
	}
	return &linuxJPEGDecoder{value: decoder}, nil
}

func (d *linuxJPEGDecoder) layout(jpeg []byte, mode Mode, downsample int) (linuxJPEGLayout, error) {
	if len(jpeg) == 0 {
		return linuxJPEGLayout{}, errors.New("empty MJPEG sample")
	}
	var layout C.inlaid_jpeg_layout
	result := C.inlaid_jpeg_layout_for(d.value, (*C.uchar)(unsafe.Pointer(&jpeg[0])), C.ulong(len(jpeg)),
		C.int(mode.Width), C.int(mode.Height), C.int(downsample), &layout)
	runtime.KeepAlive(jpeg)
	switch result {
	case 0:
		return linuxJPEGLayout{
			imageWidth: int(layout.image_width), imageHeight: int(layout.image_height),
			yWidth: int(layout.y_width), yHeight: int(layout.y_height),
			cbWidth: int(layout.cb_width), cbHeight: int(layout.cb_height),
			crWidth: int(layout.cr_width), crHeight: int(layout.cr_height),
		}, nil
	case -2:
		return linuxJPEGLayout{}, errors.New("MJPEG geometry or color layout changed during capture")
	case -3:
		return linuxJPEGLayout{}, fmt.Errorf("TurboJPEG does not support 1/%d native IDCT scaling", downsample)
	default:
		return linuxJPEGLayout{}, fmt.Errorf("read MJPEG header: %s", C.GoString(C.inlaid_jpeg_error(d.value)))
	}
}

func (d *linuxJPEGDecoder) decode(jpeg []byte, layout linuxJPEGLayout, buffers *linuxPlaneBuffers) error {
	if len(jpeg) == 0 || len(buffers.y) == 0 || len(buffers.cb) == 0 || len(buffers.cr) == 0 {
		return errors.New("invalid empty planar MJPEG decode buffer")
	}
	result := C.inlaid_jpeg_decode(d.value, (*C.uchar)(unsafe.Pointer(&jpeg[0])), C.ulong(len(jpeg)),
		(*C.uchar)(unsafe.Pointer(&buffers.y[0])), C.int(layout.yWidth),
		(*C.uchar)(unsafe.Pointer(&buffers.cb[0])), C.int(layout.cbWidth),
		(*C.uchar)(unsafe.Pointer(&buffers.cr[0])), C.int(layout.crWidth),
		C.int(layout.imageWidth), C.int(layout.imageHeight))
	runtime.KeepAlive(jpeg)
	runtime.KeepAlive(buffers)
	if result < 0 {
		return fmt.Errorf("decode MJPEG: %s", C.GoString(C.inlaid_jpeg_error(d.value)))
	}
	return nil
}

func (d *linuxJPEGDecoder) close() {
	if d != nil && d.value != nil {
		C.inlaid_jpeg_destroy(d.value)
		d.value = nil
	}
}

func nativeModeFromC(value C.inlaid_mode) linuxNativeMode {
	format := ""
	if uint32(value.pixel_format) == linuxMJPEG {
		format = "MJPG"
	}
	return linuxNativeMode{
		Mode: Mode{
			Width: int(value.width), Height: int(value.height),
			FPSNumerator: uint32(value.fps_numerator), FPSDenominator: uint32(value.fps_denominator),
			Format: format,
		},
		bufferType: uint32(value.buffer_type), formatFlag: uint32(value.format_flags), ordinal: uint32(value.ordinal),
	}
}

func nativeModeToC(value linuxNativeMode) C.inlaid_mode {
	return C.inlaid_mode{
		width: C.uint32_t(value.Width), height: C.uint32_t(value.Height),
		fps_numerator: C.uint32_t(value.FPSNumerator), fps_denominator: C.uint32_t(value.FPSDenominator),
		pixel_format: C.uint32_t(linuxMJPEG), buffer_type: C.uint32_t(value.bufferType),
		format_flags: C.uint32_t(value.formatFlag), ordinal: C.uint32_t(value.ordinal),
	}
}

func indexByte(value []byte, target byte) int {
	for index, current := range value {
		if current == target {
			return index
		}
	}
	return -1
}
