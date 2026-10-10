package wiki

import (
	"context"
	"fmt"
	"io"

	pbw "git.noncepad.com/pkg/optimizer/gen/wiki/v1"
	"google.golang.org/grpc"
)

type Transaction struct {
	stream grpc.ClientStreamingClient[pbw.PutTopicsRequest, pbw.PutTopicsResponse]
	c      *Client
	web    string
}

func (c *Client) Transaction(ctx context.Context, web string) (*Transaction, error) {
	stream, err := c.wiki.PutTopics(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to put topics stream: %s", err)
	}
	return &Transaction{
		stream: stream,
		c:      c,
		web:    web,
	}, nil
}

// Upload - upload files together with the topic
func (tx *Transaction) Upload(topicName string, fileName string, comment string, reader io.ReadCloser) error {
	var buffer [10 * 1024]byte
	var err error
	err = tx.stream.Send(&pbw.PutTopicsRequest{
		Part: &pbw.PutTopicsRequest_Attachment{
			Attachment: &pbw.PutAttachmentRequest{
				Part: &pbw.PutAttachmentRequest_Metadata{
					Metadata: &pbw.PutAttachmentMetadata{
						Web: tx.web, Topic: topicName, Attachment: fileName, Comment: comment,
					},
				},
			},
		},
	})
	if err != nil {
		err = fmt.Errorf("failed to upload file: %s", err)
		return err
	}
	var n int
out:
	for {
		n, err = reader.Read(buffer[0:])
		if err == io.EOF {
			err = nil
			break out
		} else if err != nil {
			err = fmt.Errorf("failed to read file: %s", err)
			break out
		}
		// A fresh out slice per iteration (not buffer[:n] directly) since
		// tx.stream.Send may not finish transmitting before the next
		// reader.Read call overwrites buffer's backing array. copy is
		// required here -- a real, live-confirmed bug this fixes: out
		// used to be sent as freshly make()'d, all-zero bytes, with
		// buffer's actual data never copied into it at all, so every
		// attachment ever uploaded through this path landed with its
		// real length but entirely zeroed content. Caught by
		// hedgefund.restoreFundStoreIfMissing's own round-trip test,
		// which is the first caller in this codebase to actually reopen
		// an uploaded attachment as the real file format it's supposed
		// to be, rather than just checking its size.
		out := make([]byte, n)
		copy(out, buffer[:n])
		err = tx.stream.Send(&pbw.PutTopicsRequest{
			Part: &pbw.PutTopicsRequest_Attachment{
				Attachment: &pbw.PutAttachmentRequest{
					Part: &pbw.PutAttachmentRequest_Chunk{
						Chunk: out,
					},
				},
			},
		})
		if err != nil {
			err = fmt.Errorf("failed to upload file: %s", err)
			break out
		}
	}
	if err != nil {
		return err
	}
	// send an EOF
	err = tx.stream.Send(&pbw.PutTopicsRequest{
		Part: &pbw.PutTopicsRequest_Attachment{
			Attachment: &pbw.PutAttachmentRequest{
				Part: &pbw.PutAttachmentRequest_Chunk{
					Chunk: make([]byte, 0),
				},
			},
		},
	})
	if err != nil {
		err = fmt.Errorf("failed to upload file: %s", err)
		return err
	}
	return nil
}

// Topic - put a topic
func (tx *Transaction) Topic(
	mustBeNew bool,
	appendIfPresent bool,
	parentTopic string,
	topic string,
	body string,
	comment string,
	form *Form,
	preference *Preference,
) error {
	var protoPref []*pbw.Preference
	if preference != nil {
		protoPref = preference.Proto()
	}
	tp := &pbw.PutTopicRequest{
		MustBeNew:   mustBeNew,
		Web:         tx.web,
		ParentTopic: parentTopic,
		Topic:       topic,
		Body:        body,
		Comment:     comment,
		Preferences: protoPref,
	}
	if form != nil {
		tp.FormName = form.FormTopic
		tp.Fields = make([]*pbw.Field, len(form.Row))
		for i, r := range form.Row {
			tp.Fields[i] = &pbw.Field{
				Name: "", Value: r, Title: "",
			}
		}
	}
	return tx.stream.Send(&pbw.PutTopicsRequest{
		Part: &pbw.PutTopicsRequest_Topic{
			Topic: tp,
		},
	})
}

func (tx *Transaction) Commit() (*pbw.PutTopicsResponse, error) {
	result, err := tx.stream.CloseAndRecv()
	tx.stream = nil
	if err != nil {
		return nil, fmt.Errorf("failed to save: %s", err)
	}
	return result, nil
}
